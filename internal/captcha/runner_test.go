package captcha

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// ExecRunner tests run REAL subprocesses (spawn, exit codes, ctx kill) but
// via /bin/sh with inline scripts - the error-path suite must not depend on
// node or solver.js (t7 acceptance), and /bin/sh exists on every target.

func TestExecRunnerSuccessAndShape(t *testing.T) {
	sh := "/bin/sh"
	if _, err := os.Stat(sh); err != nil {
		t.Skip("no /bin/sh")
	}
	value := strings.Repeat("C", 280)
	stdout, code, err := ExecRunner{}.Run(context.Background(), sh,
		[]string{"-c", "printf 'VERIFY_PARAM=%s\\n' " + value}, "")
	if err != nil || code != 0 {
		t.Fatalf("run: code=%d err=%v", code, err)
	}
	s := testSolver(t, &fakeRunner{results: func(int) ([]byte, int, error) { return stdout, code, err }}, nil)
	got, err := s.Solve(context.Background())
	if err != nil || got != value {
		t.Fatalf("end-to-end parse: got %q err %v", truncate(got, 20), err)
	}
}

func TestExecRunnerExitCode(t *testing.T) {
	_, code, err := ExecRunner{}.Run(context.Background(), "/bin/sh", []string{"-c", "exit 6"}, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if code != 6 {
		t.Errorf("code = %d, want 6", code)
	}
}

func TestExecRunnerTimeoutKill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, code, err := ExecRunner{}.Run(ctx, "/bin/sh", []string{"-c", "sleep 5"}, "")
	elapsed := time.Since(start)
	if code != -1 {
		t.Errorf("code = %d, want -1", code)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	// The 5s grandchild pipe hold MUST NOT breach the timeout contract:
	// WaitDelay force-closes ~100ms after the kill. Regression of this
	// bound = the silent 40s-contract breach is back.
	if elapsed > 2*time.Second {
		t.Errorf("kill took %v; WaitDelay not bounding grandchild pipes", elapsed)
	}
	// through the solver: classified as KindTimeout
	fr := &fakeRunner{results: func(int) ([]byte, int, error) { return nil, code, err }}
	_, serr := testSolver(t, fr, func(o *Options) { o.Attempts = 1 }).Solve(context.Background())
	if !errors.Is(serr, ErrTimeout) {
		t.Errorf("solver classification = %v, want ErrTimeout", serr)
	}
}

func TestExecRunnerMissingBinary(t *testing.T) {
	_, code, err := ExecRunner{}.Run(context.Background(), "/nonexistent/node-at-all", nil, "")
	if err == nil {
		t.Fatal("want spawn error")
	}
	if code != -1 {
		t.Errorf("code = %d, want -1", code)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
