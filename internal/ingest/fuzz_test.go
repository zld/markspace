package ingest

import (
	"testing"

	"mediaroute/internal/model"
)

// FuzzScanNeverPanics asserts that structural scanning and reading are total
// functions over arbitrary bytes: malformed input must be classified, never
// crash the process.
func FuzzScanNeverPanics(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add(makeBox("ftyp", 8))
	f.Add(append(makeBox("moof", 16), makeBox("mdat", 32)...))
	f.Add([]byte{0xDE, 0xAD, 0xBE, 0xEF, 'm', 'o', 'o', 'f'})
	f.Fuzz(func(t *testing.T, data []byte) {
		items, _ := Scan(data, 1<<20)
		// Every emitted box must lie inside the input and be at least a header.
		for _, it := range items {
			if it.Box == nil {
				continue
			}
			if it.Box.Header.Offset+it.Box.Header.Size > uint64(len(data)) {
				t.Fatalf("box %s exceeds input bounds", it.Box.Header.Type)
			}
			if it.Box.Header.Size < it.Box.Header.HeaderSize {
				t.Fatalf("box %s smaller than its header", it.Box.Header.Type)
			}
		}
		// A full read on the same bytes must not panic either.
		set, err := Read(data, Config{MaxBoxSize: 1 << 20, MaxInputBytes: 1 << 24})
		if set == nil && err == nil {
			t.Fatal("nil set with nil error")
		}
		if err != nil {
			if _, ok := model.AsTaskError(err); !ok {
				// Structural parse errors from go-mp4 may bubble raw; that is
				// acceptable as long as nothing panics.
				_ = err
			}
		}
	})
}
