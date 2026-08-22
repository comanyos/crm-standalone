package errors

import (
	"errors"
	"fmt"
)

type Code int

const (
	CodeUnknown Code = iota
	CodeNotFound
	CodeAlreadyExists
	CodeInvalidArgument
	CodeUnauthenticated
	CodePermissionDenied
	CodePreconditionFailed
	CodeInternal
	CodeUnavailable
	CodeFeatureGated
)

type Error struct {
	Code    Code
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

func New(code Code, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

func Wrap(code Code, msg string, cause error) *Error {
	return &Error{Code: code, Message: msg, Cause: cause}
}

func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == CodeNotFound
}

func IsAlreadyExists(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == CodeAlreadyExists
}

func IsInvalidArgument(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == CodeInvalidArgument
}

func IsUnauthenticated(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == CodeUnauthenticated
}

func IsPermissionDenied(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == CodePermissionDenied
}

func (c Code) String() string {
	switch c {
	case CodeNotFound:
		return "not_found"
	case CodeAlreadyExists:
		return "conflict"
	case CodeInvalidArgument:
		return "bad_request"
	case CodeUnauthenticated:
		return "unauthorized"
	case CodePermissionDenied:
		return "forbidden"
	case CodePreconditionFailed:
		return "precondition_failed"
	case CodeUnavailable:
		return "service_unavailable"
	case CodeFeatureGated:
		return "feature_gated"
	default:
		return "internal"
	}
}
