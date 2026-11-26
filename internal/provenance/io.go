package provenance

import (
	"io"
)

// copyBuffered streams r into w through a fixed-size buffer so hashing a
// large source does not require loading it whole into memory.
func copyBuffered(w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, 64*1024)
	var total int64
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}
