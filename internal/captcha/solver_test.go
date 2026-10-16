package captcha

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---- fake runner (the mock seam) ----------------------------------------

type call struct {
	name string
	args []string
	dir  string
}

type fakeRunner struct {
	calls   []call
	results func(attempt int) ([]byte, int, error) // 1-based
}

func (f *fakeRunner) Run(ctx context.Context, name string, args []string, dir string) ([]byte, int, error) {
	f.calls = append(f.calls, call{name: name, args: append([]string(nil), args...), dir: dir})
	return f.results(len(f.calls))
}

func goodParam(n int) string {
	return prefix + strings.Repeat("A", n) + "\n"
}

func testSolver(t *testing.T, r Runner, mut func(o *Options)) *Solver {
	t.Helper()
	opt := Options{NodePath: "/fake/node", SolverPath: "/fake/dir/solver.js", Scene: "11xygtvd", Region: "cn", Prefix: "no8xfe"}
	if mut != nil {
		mut(&opt)
	}
	s, err := NewWithRunner(opt, r)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// ---- success path --------------------------------------------------------

func TestSolveSuccessForwardingAndShape(t *testing.T) {
	fr := &fakeRunner{results: func(int) ([]byte, int, error) {
		return []byte(goodParam(280)), 0, nil
	}}
	s := testSolver(t, fr, nil)
	got, err := s.Solve(context.Background())
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	// B-16 forwards the value VERBATIM: exact bytes, only newline trimmed.
	if want := strings.Repeat("A", 280); got != want {
		t.Errorf("value not verbatim: len=%d want 280", len(got))
	}
	// argv/cwd contract: node path, then solver.js, then all three params,
	// cwd = solver directory. Empty argv would silently use solver.js
	// defaults (spike §1.6) - asserting presence is the guard.
	c := fr.calls[0]
	if len(fr.calls) != 1 {
		t.Fatalf("attempts = %d, want 1", len(fr.calls))
	}
	if c.name != "/fake/node" {
		t.Errorf("node path = %q", c.name)
	}
	wantArgs := []string{"/fake/dir/solver.js", "11xygtvd", "cn", "no8xfe"}
	if strings.Join(c.args, "|") != strings.Join(wantArgs, "|") {
		t.Errorf("argv = %q, want %q", c.args, wantArgs)
	}
	if c.dir != "/fake/dir" {
		t.Errorf("cwd = %q, want solver dir", c.dir)
	}
}

func TestSolveCRLFAndNoTrailingNewlineTolerated(t *testing.T) {
	for _, body := range []string{prefix + strings.Repeat("B", 200) + "\r\n", prefix + strings.Repeat("B", 200)} {
		fr := &fakeRunner{results: func(int) ([]byte, int, error) { return []byte(body), 0, nil }}
		if _, err := testSolver(t, fr, nil).Solve(context.Background()); err != nil {
			t.Errorf("body %q: %v", body[len(body)-2:], err)
		}
	}
}

// ---- exit-code mapping table (every branch) -------------------------------

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		code int
		want Kind
	}{
		{2, KindTimeout},
		{3, KindInitFailure},
		{4, KindSolverFail},
		{5, KindOnError},
		{6, KindInvalidParams}, // production catch-all silent failure
		{1, KindSpawnFailure},  // unlisted code: distinguishable from 6
		{137, KindSpawnFailure},
	}
	for _, tc := range cases {
		fr := &fakeRunner{results: func(int) ([]byte, int, error) { return nil, tc.code, nil }}
		_, err := testSolver(t, fr, func(o *Options) { o.Attempts = 1 }).Solve(context.Background())
		var ce *Error
		if !errors.As(err, &ce) {
			t.Fatalf("code %d: not an *Error: %v", tc.code, err)
		}
		if ce.Kind != tc.want {
			t.Errorf("code %d: Kind = %v, want %v", tc.code, ce.Kind, tc.want)
		}
		if ce.ExitCode != tc.code {
			t.Errorf("code %d: ExitCode = %d", tc.code, ce.ExitCode)
		}
	}
}

// ---- stdout format violations ---------------------------------------------

func TestBadOutputShapes(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
	}{
		{"missing prefix", "SOMETHING=abc" + strings.Repeat("x", 200) + "\n"},
		{"empty stdout", ""},
		{"value too short", prefix + "short\n"},
		{"multi-line", goodParam(280) + "noise\n"},
		{"bare prefix", prefix + "\n"},
	}
	for _, tc := range cases {
		fr := &fakeRunner{results: func(int) ([]byte, int, error) { return []byte(tc.stdout), 0, nil }}
		_, err := testSolver(t, fr, func(o *Options) { o.Attempts = 1 }).Solve(context.Background())
		var ce *Error
		if !errors.As(err, &ce) || ce.Kind != KindBadOutput {
			t.Errorf("%s: err = %v, want KindBadOutput", tc.name, err)
		}
	}
}

// ---- timeout: harness kill shares KindTimeout with solver exit 2 ----------

func TestHarnessTimeoutMapsToTimeoutKind(t *testing.T) {
	fr := &fakeRunner{results: func(int) ([]byte, int, error) {
		return nil, -1, context.DeadlineExceeded
	}}
	s := testSolver(t, fr, func(o *Options) { o.Timeout = 10 * time.Millisecond; o.Attempts = 1 })
	_, err := s.Solve(context.Background())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout kind", err)
	}
	var ce *Error
	errors.As(err, &ce)
	if !strings.Contains(err.Error(), "harness timeout") {
		t.Errorf("message lost timeout detail: %v", err)
	}
}

// ---- retry policy (load-bearing per spike: exit 6 is silent) --------------

func TestRetriesAllAttemptsThenLastError(t *testing.T) {
	fr := &fakeRunner{results: func(int) ([]byte, int, error) { return nil, 6, nil }}
	s := testSolver(t, fr, nil) // default Attempts=4
	_, err := s.Solve(context.Background())
	if len(fr.calls) != 4 {
		t.Fatalf("calls = %d, want 4 (default attempts)", len(fr.calls))
	}
	var ce *Error
	if !errors.As(err, &ce) || ce.Kind != KindInvalidParams || ce.Attempt != 4 {
		t.Errorf("last error not tagged attempt 4: %v", err)
	}
}

func TestRetrySucceedsOnThirdAttempt(t *testing.T) {
	fr := &fakeRunner{results: func(a int) ([]byte, int, error) {
		if a < 3 {
			return nil, 6, nil
		}
		return []byte(goodParam(280)), 0, nil
	}}
	if _, err := testSolver(t, fr, nil).Solve(context.Background()); err != nil {
		t.Fatalf("want success on attempt 3: %v", err)
	}
	if len(fr.calls) != 3 {
		t.Errorf("calls = %d, want 3", len(fr.calls))
	}
}

func TestParentCancellationStopsRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Slow-attempt fake: 5 ms each, so the mid-run cancellation is
	// observable as "far fewer attempts than the configured 100".
	fr := &fakeRunner{results: func(int) ([]byte, int, error) {
		select {
		case <-ctx.Done():
			return nil, -1, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
		return nil, 6, nil
	}}
	s := testSolver(t, fr, func(o *Options) { o.Attempts = 100; o.Timeout = time.Second })
	go func() { time.Sleep(40 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := s.Solve(ctx)
	if err == nil {
		t.Fatal("want error")
	}
	if time.Since(start) > 900*time.Millisecond {
		t.Errorf("did not stop promptly on cancel: %v", time.Since(start))
	}
	if fr.calls == nil || len(fr.calls) >= 100 {
		t.Errorf("kept burning attempts after cancel: %d", len(fr.calls))
	}
}

// ---- validation / wiring ---------------------------------------------------

func TestNewRejectsMissingPaths(t *testing.T) {
	if _, err := New(Options{SolverPath: "/x/solver.js"}); err == nil {
		t.Error("missing NodePath accepted")
	}
	if _, err := New(Options{NodePath: "node"}); err == nil {
		t.Error("missing SolverPath accepted")
	}
	if _, err := NewWithRunner(Options{NodePath: "n", SolverPath: "s"}, nil); err == nil {
		t.Error("nil runner accepted")
	}
}

func TestDefaults(t *testing.T) {
	s := testSolver(t, &fakeRunner{results: func(int) ([]byte, int, error) { return nil, 6, nil }}, nil)
	if s.opt.Timeout != DefaultTimeout || s.opt.Attempts != DefaultAttempts || s.opt.MinParamLen != DefaultMinParamLn {
		t.Errorf("defaults wrong: %+v", s.opt)
	}
	if s.opt.Dir != "/fake/dir" {
		t.Errorf("Dir default = %q, want parent of solver", s.opt.Dir)
	}
}

func TestSentinelIsByKind(t *testing.T) {
	fr := &fakeRunner{results: func(int) ([]byte, int, error) { return nil, 3, nil }}
	_, err := testSolver(t, fr, func(o *Options) { o.Attempts = 1 }).Solve(context.Background())
	for _, target := range []error{ErrTimeout, ErrInvalidParams, ErrBadOutput} {
		if errors.Is(err, target) {
			t.Errorf("err wrongly matches %v", target)
		}
	}
	if !errors.Is(err, ErrInitFailure) {
		t.Errorf("err should match ErrInitFailure: %v", err)
	}
}
