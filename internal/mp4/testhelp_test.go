package mp4

import "errors"

// errEOF marks end-of-buffer in the in-memory test seeker.
var errEOF = errors.New("end of buffer")
