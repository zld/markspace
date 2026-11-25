package segment

import (
	"bytes"
	"sync"
)

// jsonBuffer is a reusable encoder target. Encoders allocate on every use;
// pooling keeps repeated runs (including resume) cheap without growing
// retained memory.
type jsonBuffer struct{ b bytes.Buffer }

func (j *jsonBuffer) reset() { j.b.Reset() }
func (j *jsonBuffer) Write(p []byte) (int, error) { return j.b.Write(p) }

var jsonBufferPool = sync.Pool{
	New: func() any { return &jsonBuffer{} },
}
