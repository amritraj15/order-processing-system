package logging

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"

	"order_management/domain/shared"
)

// ErrorAttrs never formats an error's message, SQL, details or parameters.
func ErrorAttrs(err error) []any {
	kind := "internal"
	if errors.Is(err, context.Canceled) {
		kind = "canceled"
	} else if errors.Is(err, shared.ErrUnavailable) {
		kind = "unavailable"
	} else if errors.Is(err, context.DeadlineExceeded) {
		kind = "deadline"
	}
	attrs := []any{"error_type", fmt.Sprintf("%T", err)}
	var state interface{ SQLState() string }
	if errors.As(err, &state) {
		code := state.SQLState()
		valid := len(code) == 5
		for _, c := range code {
			if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
				valid = false
			}
		}
		if valid {
			if kind == "internal" {
				kind = "database"
			}
			attrs = append(attrs, "sqlstate", code, "cause_type", fmt.Sprintf("%T", state))
		}
	}
	return append(attrs, "error_kind", kind)
}

// StackFrames captures function/file/line only. Unlike raw goroutine stacks it
// contains neither argument values nor a panic's potentially sensitive value.
func StackFrames() []string {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	result := make([]string, 0, n)
	for {
		f, more := frames.Next()
		result = append(result, fmt.Sprintf("%s %s:%d", f.Function, filepath.Base(f.File), f.Line))
		if !more {
			break
		}
	}
	return result
}
