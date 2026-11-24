package container

import (
	"fmt"
	"os"
)

// openLimited opens a local file after enforcing the input size ceiling.
// Files are opened read-only: the kernel never rewrites sources.
func openLimited(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	stat, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if stat.Size() > MaxInputBytes {
		_ = f.Close()
		return nil, fmt.Errorf("input %d bytes exceeds limit %d", stat.Size(), MaxInputBytes)
	}
	return f, nil
}

// HasBoxType reports whether the pre-scan saw a non-truncated box of the
// given type.
func (p *PreScan) HasBoxType(typ string) bool {
	for _, b := range p.Boxes {
		if b.Type == typ && !b.Truncated {
			return true
		}
	}
	return false
}

// BoxesUnder returns pre-scan records of the given type at or below the
// given depth.
func (p *PreScan) BoxesUnder(typ string) []BoxRecord {
	var out []BoxRecord
	for _, b := range p.Boxes {
		if b.Type == typ {
			out = append(out, b)
		}
	}
	return out
}
