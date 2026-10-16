package captcha

import "strconv"

// Kind classifies solver failures into distinguishable Go error values.
// The mapping table is the t2 black-box contract (exit code semantics);
// KindSpawnTimeout covers OUR timeout kill (the solver hangs silently,
// spike §9.5), which the process' own exit-2 timeout maps to as well -
// both are indistinguishable root causes, one kind.
type Kind int

const (
	// KindTimeout: exit 2 (solver self-reported) OR our 40s kill.
	KindTimeout Kind = iota
	// KindInitFailure: exit 3 (solver initialization failure).
	KindInitFailure
	// KindSolverFail: exit 4 (SDK "fail" callback).
	KindSolverFail
	// KindOnError: exit 5 (SDK "onError" callback).
	KindOnError
	// KindInvalidParams: exit 6. In production this code is the CATCH-ALL
	// silent failure (network down included - spike §1.6 "无网络即失败 →
	// exit 6 双空"), not a trustworthy "parameters were wrong" signal.
	KindInvalidParams
	// KindBadOutput: exit 0 but stdout violated the VERIFY_PARAM contract
	// (missing prefix, empty/multi-line, value shorter than MinParamLen).
	KindBadOutput
	// KindSpawnFailure: the process could not be executed at all (node
	// missing, EACCES, ...) or an unlisted exit code appeared.
	KindSpawnFailure
)

func (k Kind) String() string {
	switch k {
	case KindTimeout:
		return "timeout"
	case KindInitFailure:
		return "init-failure"
	case KindSolverFail:
		return "solver-fail"
	case KindOnError:
		return "on-error"
	case KindInvalidParams:
		return "invalid-params-or-silent-failure"
	case KindBadOutput:
		return "bad-output"
	case KindSpawnFailure:
		return "spawn-failure"
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

// Error is the single concrete failure type; callers classify with
// errors.As + the Kind field (or errors.Is against the sentinels below).
type Error struct {
	Kind     Kind
	ExitCode int // solver exit code; -1 when the process never ran / unknown
	Attempt  int // 1-based attempt on which this failure surfaced
	Err      error
	Detail   string
}

func (e *Error) Error() string {
	msg := "captcha solver: " + e.Kind.String()
	if e.ExitCode >= 0 {
		msg += " (exit " + strconv.Itoa(e.ExitCode) + ")"
	}
	if e.Attempt > 0 {
		msg += " on attempt " + strconv.Itoa(e.Attempt)
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	} else if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes the underlying transport error, if any.
func (e *Error) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrTimeout) etc. work by Kind.
func (e *Error) Is(target error) bool {
	ot, ok := target.(*Error)
	return ok && e.Kind == ot.Kind
}

// Sentinel errors for errors.Is comparisons, one per Kind.
var (
	ErrTimeout       = &Error{Kind: KindTimeout}
	ErrInitFailure   = &Error{Kind: KindInitFailure}
	ErrSolverFail    = &Error{Kind: KindSolverFail}
	ErrOnError       = &Error{Kind: KindOnError}
	ErrInvalidParams = &Error{Kind: KindInvalidParams}
	ErrBadOutput     = &Error{Kind: KindBadOutput}
	ErrSpawnFailure  = &Error{Kind: KindSpawnFailure}
)

// classifyExit maps a solver exit code to a Kind per the frozen table.
// ok=false means "exit 0" (success path handled by the caller).
func classifyExit(code int) (Kind, bool) {
	switch code {
	case 0:
		return 0, false
	case 2:
		return KindTimeout, true
	case 3:
		return KindInitFailure, true
	case 4:
		return KindSolverFail, true
	case 5:
		return KindOnError, true
	case 6:
		return KindInvalidParams, true
	default:
		return KindSpawnFailure, true // unlisted codes: undiagnosable, keep distinguishable from 6
	}
}
