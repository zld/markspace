package errs

import stderrors "errors"

// AsError is the standard errors.As wrapper kept under a distinct name so
// callers of this package do not have to alias two "errors" imports.
func AsError(err error, target **Error) bool {
	return stderrors.As(err, target)
}

// Is reports whether any error in err's chain matches code.
func Is(err error, code Code) bool {
	var e *Error
	if !stderrors.As(err, &e) {
		return false
	}
	return e.Code == code
}
