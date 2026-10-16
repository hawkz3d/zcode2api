// Command zhipucode2api is the Go rewrite of the legacy Python zcode2api
// gateway. It reuses the same configuration contract and the same SQLite
// database, so it can be run side by side with the legacy service on another
// port and compared endpoint-for-endpoint.
//
// Usage:
//
//	zhipucode2api serve          start the HTTP server
//	zhipucode2api -h | help      this text
//
// Configuration comes from internal/config (env-contract.md §1):
//
//	ZCODE_PORT      listen port        (default 3000)
//	ZCODE_HOST      listen address     (default 0.0.0.0)
//	ZCODE_DATA_DIR  data directory     (default data)
//
// The database is <ZCODE_DATA_DIR>/accounts.db.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"zhipucode2api/internal/adminapi"
	"zhipucode2api/internal/captcha"
	"zhipucode2api/internal/config"
	"zhipucode2api/internal/gateway"
	"zhipucode2api/internal/pages"
	"zhipucode2api/internal/store"
	"zhipucode2api/internal/upstream"
)

// EnvAllowEmptyPool is the explicit opt-in that lets a green-field deployment
// start from zero accounts. It lives here rather than in internal/config
// because it gates a startup SAFETY decision, not service configuration - and
// the guard that reads it is the only thing allowed to act on it. The default
// (unset, or any value other than "1") is refusal: ADR-005 treats an empty
// pool as an accident to be caught, not a state to be tolerated.
const EnvAllowEmptyPool = "ZCODE_ALLOW_EMPTY_POOL"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "zhipucode2api: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage(os.Stderr)
		return errors.New("missing subcommand")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

func usage(w *os.File) {
	fmt.Fprint(w, `zhipucode2api - Go gateway for the ZCode account pool

Usage:
  zhipucode2api serve [flags]    start the HTTP server
  zhipucode2api help             show this message

Environment (env-contract.md §1):
  ZCODE_PORT       listen port        (default 3000)
  ZCODE_HOST       listen address     (default 0.0.0.0)
  ZCODE_DATA_DIR   data directory     (default data; DB is <dir>/accounts.db)

The listen address can also be given with -addr host:port, which overrides
ZCODE_HOST/ZCODE_PORT for local verification runs.
`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "", "listen address host:port (overrides ZCODE_HOST/ZCODE_PORT)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// ADR-005 encoded as a startup invariant: a RELATIVE ZCODE_DATA_DIR is
	// resolved against the process cwd, so the same unit silently talks to a
	// different database depending on where it was started - the A2 form of
	// the accident (empty database, zero accounts, no alarm). Refuse to start.
	if !filepath.IsAbs(cfg.DataDir) {
		return fmt.Errorf("ZCODE_DATA_DIR must be an absolute path, got %q "+
			"(a relative path drifts with the cwd - see ADR-005)", cfg.DataDir)
	}

	// The pool store is opened READ-WRITE by the server: the account-rotation
	// seam currently only reads, but the full pool task will need to write
	// cooling/exhausted state, and opening rw now keeps the seam honest about
	// what the store can do. No write happens on the gateway request path in
	// this task.
	ctx := context.Background()
	dbPath := cfg.AccountsDB()

	// Capture the file's identity BEFORE opening: store.Open creates the file
	// when it is missing (it does NOT create the schema). This flag no longer
	// drives the guard's JUDGEMENT - only whether the guard may clean up the
	// artefact it just created (see guardPoolDatabase and the t14 note).
	_, statErr := os.Stat(dbPath)
	freshFile := os.IsNotExist(statErr)

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open database %s: %w", dbPath, err)
	}

	// Green-field bootstrap. This call is NOT a convenience and must not be
	// "tidied away" as one - it is the other half of the opt-in, and the two
	// conditions below are what keep the guard below from being bypassable.
	//
	// Why the schema has to be created here at all:
	//   store.Open creates the FILE but never the TABLES. A guard that refuses
	//   an empty pool would therefore also refuse a legitimately-empty new
	//   deployment, with no way out: the operator says "start from zero", and
	//   the process still dies on "no such table: accounts". Without this step
	//   the opt-in is not a working promise, only a way to get a different
	//   error message. Creating the schema here is what makes "I mean to begin
	//   empty" actually achievable, which is exactly the scenario ADR-005
	//   discusses (a real new deployment, not the A2 misconfiguration).
	//
	// Why it must be gated on freshFile && allowEmptyPool(), in that order:
	//   freshFile alone would mean "whatever I find, I may re-create" - and a
	//   leftover schema-less database (the artefact the t14 defect produced)
	//   would be silently blessed into a valid empty pool. allowEmptyPool()
	//   alone would mean "opt-in lets me write schema anywhere", including
	//   into an existing database whose schema an operator may still want to
	//   inspect before it is touched. Together they say the only thing worth
	//   saying: a file THIS process just created, in a deployment that
	//   explicitly declared it starts from zero. A pre-existing database is
	//   never modified - it is only ever read, judged, and (if it fails the
	//   guard) left exactly as it was found.
	if freshFile && allowEmptyPool() {
		if err := st.EnsureSchema(ctx); err != nil {
			return failStartup(st, dbPath, freshFile,
				fmt.Errorf("initialise schema for a green-field pool: %w", err))
		}
	}

	poolSize, err := guardPoolDatabase(ctx, dbPath, st)
	if err != nil {
		return failStartup(st, dbPath, freshFile, err)
	}
	defer st.Close()

	// The monitoring ring is owned by the gateway (the only writer) and merely
	// observed by the admin API through its injectable hooks - so the gateway
	// never imports internal/adminapi, and B-21/B-22 read the same live ring.
	reqLog := gateway.NewReqLog()

	// Captcha solver (I-3). ONE solver instance serves both links, but the two
	// links use it with OPPOSITE timing, which is the whole point of the frozen
	// contract (review-evidence/i3-captcha-lazy-contract.md §2):
	//   - gateway: LAZY. The solver is only consulted after a challenge; a
	//     challenge-free request never calls it (asserted by G-1 in
	//     internal/gateway/captcha_test.go).
	//   - claim:   EAGER. `POST /admin/api/claim` solves on every request.
	// Wiring one instance to both is safe precisely because the TIMING lives at
	// the call sites, not in the solver.
	//
	// A missing solver is NOT fatal: deployments without the black-box script
	// keep serving (the gateway simply relays a challenge unchanged). The
	// absence is logged so it is observable rather than silent.
	solver, serr := newCaptchaSolver(cfg)
	if serr != nil {
		log.Printf("captcha: solver unavailable (%v) - challenge retries disabled", serr)
	}

	gw := gateway.New(st, st, upstream.New(), gateway.Options{ReqLog: reqLog, Captcha: solver})
	handler := newRootMux(gw.Handler(), st, st, reqLog, solver)

	// Probe the upstream URL resolution once at startup so a misconfigured
	// environment shows up in the log rather than in the first user request.
	// The pool size is part of the line by contract (cutover-checklist C-3):
	// it turns the ADR-005 A2 accident from SILENT into OBSERVABLE.
	log.Printf("zhipucode2api listening on %s (db=%s, pool: %d accounts)",
		listenAddr(cfg, *addr), dbPath, poolSize)

	srv := &http.Server{
		Addr:              listenAddr(cfg, *addr),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: SSE responses are long-lived by contract
		// (the legacy gateway has no upstream timeout either).
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	idle := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Print("zhipucode2api shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		close(idle)
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen: %w", err)
	}
	<-idle
	return nil
}

// newRootMux is the integration point between the three routing packages.
// Design (measured disjoint surfaces):
//   - "/v1/" subtree -> the gateway handler (its own mux carries the three
//     exact /v1 routes, its /v1/ and / catch-alls answer shape-A 404, and its
//     slashRedirect keeps /v1/models/ -> 307 ABSOLUTE exactly as measured);
//   - "/admin/api" and "/admin/api/" -> adminapi (t13). Registered as an exact
//     path PLUS a subtree so Go's ServeMux never emits its own 307 for the
//     bare prefix: the legacy service answers 404 for both forms, and the
//     adminapi router reproduces that body. A bare subtree registration would
//     instead give 307 then 200 - the trap t17 measured.
//   - everything else -> pages (Register installs "/", the /admin pages,
//     /static assets and the shape-A 404 catch-all for unknown paths such as
//     /health, matching the legacy 404 detail body).
//
// Go's ServeMux matches the MOST SPECIFIC pattern, so pages' "/" catch-all
// can never swallow /v1/* - asserted by wiring_test.
func newRootMux(gwHandler http.Handler, accounts adminapi.AccountSource, settings adminapi.SettingSource, reqLog *gateway.ReqLog, solver *captcha.Solver) *http.ServeMux {
	mux := http.NewServeMux()
	pages.Register(mux)
	mux.Handle("/v1/", gwHandler)
	adminapi.Register(mux, accounts, settings, adminapi.Options{
		Monitoring:      reqLog.Snapshot,
		MonitoringClear: reqLog.Clear,
		Solver:          solver,
	})
	return mux
}

// listenAddr resolves the bind address, letting -addr win over the environment.
func listenAddr(cfg config.Config, override string) string {
	if override != "" {
		return override
	}
	return fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
}

// allowEmptyPool reports whether a green-field deployment explicitly opted in
// to starting from zero accounts.
//
// ADR-005's whole point is that an empty pool must never be silent, so the
// default is REFUSAL: a deployment that really means to begin empty says so in
// its unit file (ZCODE_ALLOW_EMPTY_POOL=1), and that declaration is the only
// thing that distinguishes intent from the A2 accident. Anything other than
// the literal "1" (including unset) leaves the guard in its refusing mode.
func allowEmptyPool() bool {
	return os.Getenv(EnvAllowEmptyPool) == "1"
}

// guardPoolDatabase implements the ADR-005 A2 guard, and returns the pool size
// it observed so the startup line can report it.
//
// t14 (repair): the previous version returned early unless THIS process had
// just created the file. That bound the guard's whole effect to a one-shot
// fact, while store.Open creates the FILE without the SCHEMA - so the very act
// of intercepting left behind a 4096-byte table-less database, and every
// subsequent start saw freshFile=false and was let through. Combined with the
// production unit's Restart=always/RestartSec=5, the A2 accident was merely
// postponed by one restart and then reported as a healthy service. The check
// is now UNCONDITIONAL, so the three states below are all fatal:
//
//	file created by this process, no tables -> FATAL (and the artefact is removed)
//	file already present, no accounts table -> FATAL
//	file present, table present, 0 rows    -> FATAL unless opted in
//
// The returned count is meaningful only when err is nil.
func guardPoolDatabase(ctx context.Context, dbPath string, st *store.Store) (int, error) {
	accts, listErr := st.ListAccounts(ctx)
	switch {
	case listErr != nil:
		// store.Open creates the file but not the schema, so a table-less
		// database surfaces here rather than as an empty list. This is the
		// shape the old freshFile guard produced and then walked past.
		return 0, fmt.Errorf("database %s has no usable accounts table (%v) - "+
			"ZCODE_DATA_DIR is probably wrong, or the file is not a zcode2api database (ADR-005 A2)", dbPath, listErr)
	case len(accts) == 0 && !allowEmptyPool():
		return 0, fmt.Errorf("database %s holds 0 accounts - "+
			"ZCODE_DATA_DIR is probably wrong (ADR-005 A2); set %s=1 only if this is a "+
			"deliberate green-field deployment", dbPath, EnvAllowEmptyPool)
	}
	return len(accts), nil
}

// failStartup reports a refused startup, removing the artefact when this
// process is the one that created it. Removing the file is what makes the
// interception IDEMPOTENT: the next start re-creates it and is refused again,
// instead of finding a leftover schema-less database and being let through.
//
// An existing database is NEVER removed, however broken it looks - it may be
// the operator's real pool behind a wrong ZCODE_DATA_DIR, and destroying
// evidence is worse than a loud startup failure.
func failStartup(st *store.Store, dbPath string, freshFile bool, cause error) error {
	// Close before unlinking so the removal cannot race an open handle.
	_ = st.Close()
	if freshFile {
		if err := removeSelfCreatedDB(dbPath); err != nil {
			log.Printf("warning: could not remove the database this process just created: %v", err)
		}
	}
	return cause
}

// removeSelfCreatedDB deletes the database file together with the WAL sidecars
// store.Open may have produced. Only the three files SQLite itself names are
// touched, and only for a path the caller already knows this process created.
func removeSelfCreatedDB(dbPath string) error {
	var firstErr error
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// newCaptchaSolver assembles the black-box captcha solver from config.
//
// Paths are INJECTED, never hardcoded (t7 rule 5/6): the node binary comes from
// ZCODE_NODE_PATH (config.Config.NodePath) and the script is resolved relative
// to this repository's deployment layout, overridable by ZCODE_SOLVER_PATH so
// the binary can be relocated without a rebuild. The scene/region/prefix triple
// is the B-15 config, whose defaults were measured live:
// scene_id 11xygtvd / region cn / prefix no8xfe.
func newCaptchaSolver(cfg config.Config) (*captcha.Solver, error) {
	solverPath := envOr("ZCODE_SOLVER_PATH", "")
	if solverPath == "" {
		// Default: the legacy deployment's captcha_node/solver.js, which the
		// old service still hosts (the black-box subprocess contract is
		// unchanged by this rewrite - ADR: solver.js stays an external dep).
		solverPath = filepath.Join(filepath.Dir(cfg.DataDir), "..", "zcode2api", "captcha_node", "solver.js")
	}
	return captcha.New(captcha.Options{
		NodePath:   cfg.NodePath,
		SolverPath: solverPath,
		Scene:      "11xygtvd",
		Region:     "cn",
		Prefix:     "no8xfe",
		Timeout:    40 * time.Second,
		Attempts:   4,
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
