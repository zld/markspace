package pipeline

import (
	"encoding/binary"
	"fmt"
	"os"

	"mediatimeline/asset"
	"mediatimeline/evidence"
)

// readMDAT returns the mdat payload bytes of an MP4 file by scanning top-level
// boxes; only the needed payload is held in memory, one file at a time.
func readMDAT(path string) []byte {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	start, end, ok := findTopBox(raw, "mdat")
	if !ok {
		return nil
	}
	return raw[start:end]
}

func findTopBox(raw []byte, boxType string) (start, end int, ok bool) {
	for off := 0; off+8 <= len(raw); {
		size := int(binary.BigEndian.Uint32(raw[off:]))
		typ := string(raw[off+4 : off+8])
		if size == 0 || off+size > len(raw) {
			return 0, 0, false
		}
		if typ == boxType {
			return off + 8, off + size, true
		}
		off += size
	}
	return 0, 0, false
}

// splitMDATPayloads slices the mdat payload into per-sample buffers using the
// trun-derived sizes and offsets stored on the parsed fragment.
func splitMDATPayloads(frag *asset.Fragment, mdat []byte) [][]byte {
	var out [][]byte
	for _, s := range frag.Samples {
		end := s.PayloadOff + int64(s.Size)
		if end > int64(len(mdat)) {
			return out // truncated tail: keep the frames already verified
		}
		out = append(out, mdat[s.PayloadOff:end])
	}
	return out
}

func encodePPMPublic(f *evidence.Frame) []byte {
	header := fmt.Sprintf("P6\n%d %d\n255\n", f.Width, f.Height)
	out := make([]byte, 0, len(header)+len(f.Pixels))
	out = append(out, header...)
	out = append(out, f.Pixels...)
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}
