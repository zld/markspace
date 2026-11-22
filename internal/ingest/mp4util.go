package ingest

import (
	"fmt"
	"strconv"
)

type plainError string

func (p plainError) Error() string { return string(p) }

func mperr(format string, args ...interface{}) error {
	return plainError(fmt.Sprintf(format, args...))
}

func itoa(i int) string { return strconv.Itoa(i) }

func i64s(i int64) string { return strconv.FormatInt(i, 10) }

type boxRange struct{ offset, size uint64 }

// findMdat locates the first mdat box at the top level.
func findMdat(data []byte) *boxRange {
	for i := uint64(0); i+8 <= uint64(len(data)); {
		size := uint64(data[i])<<24 | uint64(data[i+1])<<16 | uint64(data[i+2])<<8 | uint64(data[i+3])
		typ := string(data[i+4 : i+8])
		if size == 1 && i+16 <= uint64(len(data)) {
			size = be64(data[i+8 : i+16])
			if typ == "mdat" {
				return &boxRange{offset: i + 16, size: size - 16}
			}
		} else if typ == "mdat" {
			return &boxRange{offset: i + 8, size: size - 8}
		}
		if size < 8 || i+size > uint64(len(data)) {
			return nil
		}
		i += size
	}
	return nil
}

func be64(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}
