package media

import "io"

// limitedReader wraps a reader and fails with a structured oversize error once
// more than limit bytes have been consumed. Every file entry point uses it so
// the 64 MiB input budget is enforced uniformly.
type limitedReader struct {
	r     io.Reader
	left  int64
	limit int64
}

// LimitedReader caps r at limit bytes (media.MaxInputBytes by convention).
func LimitedReader(r io.Reader, limit int64) io.Reader {
	return &limitedReader{r: r, left: limit + 1, limit: limit}
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, &Error{
			Code:   CodeOversize,
			Source: "input",
			Detail: "input exceeds configured limit",
			Offset: l.limit,
		}
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left <= 0 && err == nil {
		return n, &Error{
			Code:   CodeOversize,
			Source: "input",
			Detail: "input exceeds configured limit",
			Offset: l.limit,
		}
	}
	return n, err
}

// ReadAllLimited reads at most limit bytes from r.
func ReadAllLimited(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(LimitedReader(r, limit))
}
