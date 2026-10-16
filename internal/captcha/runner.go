package captcha

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// Runner executes one solver process attempt. It is THE mock seam (t6
// lesson re-applied here): every error-path unit test drives scripted
// (stdout, exitCode, err) tuples through a fake, no node or solver.js
// needed. ExecRunner is the production implementation; runner_test.go
// exercises it against /bin/sh (real spawn, still no solver.js).
type Runner interface {
	// Run starts name with args in dir ("" = inherit cwd), waits for
	// completion or ctx cancellation, and returns captured stdout plus the
	// exit code (-1 if the process never ran or ctx killed it before
	// exit). A ctx-caused termination must surface as an err that
	// errors.Is matches context.DeadlineExceeded/context.Canceled.
	Run(ctx context.Context, name string, args []string, dir string) (stdout []byte, exitCode int, err error)
}

// ExecRunner runs the solver as a real subprocess via os/exec.
type ExecRunner struct{}

// Compile-time check.
var _ Runner = ExecRunner{}

// Run implements Runner. stderr is captured and discarded from the result
// (the contract's failure mode is "both streams empty"; keeping it would
// tempt callers into trusting diagnostics the solver does not produce).
//
// WaitDelay bounds the kill: on ctx cancellation exec kills the direct
// child, but a grandchild (solver.js spawns node sub-resources) can keep
// the stdout pipe open and pin cmd.Wait until the pipe drains naturally -
// which would silently breach the 40s timeout contract. After the child is
// gone, WaitDelay force-closes the pipes and returns the cancellation
// error instead of hanging.
func (ExecRunner) Run(ctx context.Context, name string, args []string, dir string) ([]byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.WaitDelay = 100 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		// ctx kill: normalize to the sentinel the classifier expects.
		switch {
		case errors.Is(err, exec.ErrNotFound):
			return stdout.Bytes(), -1, err
		case ctx.Err() != nil:
			return stdout.Bytes(), -1, ctx.Err()
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout.Bytes(), ee.ExitCode(), nil
		}
		return stdout.Bytes(), -1, err
	}
	return stdout.Bytes(), 0, nil
}
