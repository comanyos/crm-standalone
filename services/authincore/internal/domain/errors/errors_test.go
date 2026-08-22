package errors_test

import (
	"errors"
	"fmt"
	"testing"

	domainerr "github.com/authincore/authincore/internal/domain/errors"
)

func TestNew(t *testing.T) {
	err := domainerr.New(domainerr.CodeNotFound, "thing not found")
	if err.Code != domainerr.CodeNotFound {
		t.Errorf("expected CodeNotFound")
	}
	if err.Error() != "thing not found" {
		t.Errorf("unexpected message: %s", err.Error())
	}
}

func TestWrap_Unwrap(t *testing.T) {
	cause := fmt.Errorf("db error")
	err := domainerr.Wrap(domainerr.CodeInternal, "store failed", cause)

	if !errors.Is(err, cause) {
		t.Error("expected errors.Is to find cause via Unwrap")
	}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

func TestIs_Helpers(t *testing.T) {
	cases := []struct {
		code  domainerr.Code
		check func(error) bool
		name  string
	}{
		{domainerr.CodeNotFound, domainerr.IsNotFound, "IsNotFound"},
		{domainerr.CodeAlreadyExists, domainerr.IsAlreadyExists, "IsAlreadyExists"},
		{domainerr.CodeInvalidArgument, domainerr.IsInvalidArgument, "IsInvalidArgument"},
		{domainerr.CodeUnauthenticated, domainerr.IsUnauthenticated, "IsUnauthenticated"},
		{domainerr.CodePermissionDenied, domainerr.IsPermissionDenied, "IsPermissionDenied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := domainerr.New(tc.code, "msg")
			if !tc.check(err) {
				t.Errorf("%s returned false for matching error", tc.name)
			}
			other := domainerr.New(domainerr.CodeInternal, "other")
			if tc.check(other) {
				t.Errorf("%s returned true for non-matching error", tc.name)
			}
			if tc.check(fmt.Errorf("plain error")) {
				t.Errorf("%s returned true for plain error", tc.name)
			}
		})
	}
}
