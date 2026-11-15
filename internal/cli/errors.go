package cli

import "errors"

// errInvalid marks parameter or input validation failures (exit code 2).
var errInvalid = errors.New("invalid argument")

// IsInvalid reports whether err originates from CLI validation.
func IsInvalid(err error) bool {
	return errors.Is(err, errInvalid)
}
