package app

import "errors"

// ErrInternal marks an internal processing or cancellation-propagation
// failure (command exit 3), as opposed to damaged input or a mapping error
// (exit 2), which is reported as a verdict rather than an error.
var ErrInternal = errors.New("app: internal invariant failure")
