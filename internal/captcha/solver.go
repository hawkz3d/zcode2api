package captcha

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// Contract defaults (env-contract.md E-13; spike §9.5: both timers/retries
// live on THIS side because the solver fails silently).
const (
	DefaultTimeout    = 40 * time.Second
	DefaultAttempts   = 4
	DefaultMinParamLn = 128 // measured value is 280 B; 128 rejects truncation/garbage with margin
	prefix            = "VERIFY_PARAM="
)

// Options fully configure a Solver. Paths are INJECTED (t7 rule 5/6):
// NodePath comes from config.Config.NodePath (ZCODE_NODE_PATH), SolverPath
// from deployment config - neither is hardcoded here.
type Options struct {
	NodePath   string // e.g. cfg.NodePath ("node" default from env)
	SolverPath string // path to solver.js (black box)
	Dir        string // process cwd; default filepath.Dir(SolverPath) (spike: run from captcha_node/)

	Scene  string // B-15 scene_id, e.g. "11xygtvd"
	Region string // B-15 region, e.g. "cn"
	Prefix string // B-15 prefix, e.g. "no8xfe"

	Timeout  time.Duration // per attempt, default 40s
	Attempts int           // TOTAL attempts per Solve (see CR-21 note in doc.go), default 4

	MinParamLen int // reject shorter VERIFY_PARAM values, default 128
}

// Solver calls the black-box solver under timeout with retries.
type Solver struct {
	opt    Options
	runner Runner
}

// New validates options (fills defaults) and binds the real ExecRunner.
func New(opt Options) (*Solver, error) {
	return NewWithRunner(opt, ExecRunner{})
}

// NewWithRunner is the DI seam for tests and future pooling.
func NewWithRunner(opt Options, r Runner) (*Solver, error) {
	if r == nil {
		return nil, &Error{Kind: KindSpawnFailure, Detail: "nil runner"}
	}
	if opt.NodePath == "" {
		return nil, &Error{Kind: KindSpawnFailure, Detail: "NodePath required (from config ZCODE_NODE_PATH)"}
	}
	if opt.SolverPath == "" {
		return nil, &Error{Kind: KindSpawnFailure, Detail: "SolverPath required (black-box path, never hardcoded here)"}
	}
	if opt.Dir == "" {
		opt.Dir = filepath.Dir(opt.SolverPath)
	}
	if opt.Timeout <= 0 {
		opt.Timeout = DefaultTimeout
	}
	if opt.Attempts <= 0 {
		opt.Attempts = DefaultAttempts
	}
	if opt.MinParamLen <= 0 {
		opt.MinParamLen = DefaultMinParamLn
	}
	return &Solver{opt: opt, runner: r}, nil
}

// argv assembles the frozen black-box interface: solver then the three
// params. Never empty-argv: solver.js would silently use its own baked-in
// defaults (spike §1.6) which is NOT equivalent to configured B-15 values.
func (s *Solver) argv() []string {
	return []string{s.opt.SolverPath, s.opt.Scene, s.opt.Region, s.opt.Prefix}
}

// Solve runs the solver process with per-attempt timeout, retrying every
// failure class (exit 6 is the unobservable catch-all - the spike is
// explicit that retries are load-bearing). It returns the first stdout
// VERIFY_PARAM value that passes format+length validation, forwarded
// verbatim for upstream header use (B-16).
func (s *Solver) Solve(ctx context.Context) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= s.opt.Attempts; attempt++ {
		actx, cancel := context.WithTimeout(ctx, s.opt.Timeout)
		stdout, code, rerr := s.runner.Run(actx, s.opt.NodePath, s.argv(), s.opt.Dir)
		cancel()

		param, err := s.interpret(stdout, code, rerr, attempt)
		if err == nil {
			return param, nil
		}
		lastErr = err
		// Parent cancellation: stop immediately, do not burn attempts.
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return "", lastErr
		}
	}
	return "", lastErr
}

// interpret turns one attempt's outcome into (verifyParam, nil) or a
// classified *Error, per the frozen exit-code table.
func (s *Solver) interpret(stdout []byte, code int, rerr error, attempt int) (string, error) {
	if rerr != nil {
		if errors.Is(rerr, context.Canceled) {
			return "", &Error{Kind: KindSpawnFailure, ExitCode: -1, Attempt: attempt, Err: rerr, Detail: "cancelled"}
		}
		// Our per-attempt kill (DeadlineExceeded) shares KindTimeout with
		// the solver's self-reported exit 2: same unobservable root cause.
		if errors.Is(rerr, context.DeadlineExceeded) {
			return "", &Error{Kind: KindTimeout, ExitCode: -1, Attempt: attempt, Err: rerr, Detail: "harness timeout after " + s.opt.Timeout.String()}
		}
		return "", &Error{Kind: KindSpawnFailure, ExitCode: -1, Attempt: attempt, Err: rerr}
	}
	if kind, failed := classifyExit(code); failed {
		return "", &Error{Kind: kind, ExitCode: code, Attempt: attempt}
	}
	return parseVerifyParam(stdout, s.opt.MinParamLen, attempt)
}

// parseVerifyParam enforces the stdout contract: exactly one line, the
// literal VERIFY_PARAM= prefix, a value of at least minLen, and no
// rewriting of the value (B-16 forwards it verbatim upstream).
func parseVerifyParam(stdout []byte, minLen, attempt int) (string, error) {
	text := strings.TrimRight(string(stdout), "\r\n")
	if strings.ContainsAny(text, "\n\r") {
		return "", &Error{Kind: KindBadOutput, ExitCode: 0, Attempt: attempt, Detail: "stdout must be a single line"}
	}
	raw, ok := strings.CutPrefix(text, prefix)
	if !ok {
		return "", &Error{Kind: KindBadOutput, ExitCode: 0, Attempt: attempt, Detail: "missing VERIFY_PARAM= prefix"}
	}
	if len(raw) < minLen {
		return "", &Error{Kind: KindBadOutput, ExitCode: 0, Attempt: attempt, Detail: "value too short: " + itoa(len(raw)) + " < " + itoa(minLen)}
	}
	return raw, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
