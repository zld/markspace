package timecode

import "archivecheck/internal/fault"

// asFault narrows an error to the shared fault model for assertions.
func asFault(err error) *fault.Err { return fault.As(err) }
