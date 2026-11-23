package kernel

import "errors"

var (
	errNoInputs             = errors.New("provenance record names no inputs")
	errConfigDigestMismatch = errors.New("manifest config block does not match its config digest")
)
