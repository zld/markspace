package cli

import "io"

// ioWriter is the minimal sink used for informational encoder messages.
type ioWriter = io.Writer
