package stream

import "io"

// errEOF is returned by Pop once a closed pipe has been fully drained.
var errEOF = io.EOF
