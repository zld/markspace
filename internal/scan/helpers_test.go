package scan

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func writeFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }

func writeU32(dst []byte, v uint32) { binary.BigEndian.PutUint32(dst, v) }

// locate recursively finds the first box of typ, descending into containers.
func locate(data []byte, typ string) (int64, uint64, error) {
	var found int64 = -1
	var foundSize uint64
	var walk func(start, end int64) error
	walk = func(start, end int64) error {
		r := bytes.NewReader(data[start:end])
		for pos := int64(0); pos+8 <= end-start; {
			var hdr [8]byte
			if _, err := r.ReadAt(hdr[:], pos); err != nil {
				return err
			}
			sz := uint64(binary.BigEndian.Uint32(hdr[:4]))
			name := string(hdr[4:])
			if name == typ && found < 0 {
				found, foundSize = start+pos, sz
			}
			if containerType(name) {
				if err := walk(start+pos+8, start+pos+int64(sz)); err != nil {
					return err
				}
			}
			pos += int64(sz)
		}
		return nil
	}
	if err := walk(0, int64(len(data))); err != nil {
		return 0, 0, err
	}
	if found < 0 {
		return 0, 0, fmt.Errorf("box %s not found", typ)
	}
	return found, foundSize, nil
}
