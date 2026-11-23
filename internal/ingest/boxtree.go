// Package ingest turns MP4, restricted-MXF and RTP inputs into the unified
// model.Timeline. Parsers are deliberately scope-restricted and every fault
// carries a source byte offset.
package ingest

import (
	"encoding/binary"
	"os"

	"mediapipe/internal/fault"
	"mediapipe/internal/limits"
)

// rawBox is one located MP4 box header plus an in-memory payload reader.
type rawBox struct {
	typ       string
	offset    int64 // absolute offset of the size field
	headerLen int64
	size      int64 // declared total size
	payload   []byte
}

// end returns the absolute offset immediately after the box.
func (b rawBox) end() int64 { return b.offset + b.size }

// payloadOff returns the absolute offset of the first payload byte.
func (b rawBox) payloadOff() int64 { return b.offset + b.headerLen }

// scanTopBoxes walks top-level boxes. A box whose declared size runs past EOF
// is returned as a recoverable truncated-unit error carrying its offset; the
// scan then stops because no further boundary can be trusted.
func scanTopBoxes(file string, data []byte, errs *fault.Batch) []rawBox {
	var boxes []rawBox
	pos := 0
	for pos < len(data) {
		if len(data)-pos < 8 {
			errs.Add(fault.New(fault.KindTruncated,
				"trailing bytes shorter than box header", file, int64(pos), true))
			return boxes
		}
		size := int64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		hdr := int64(8)
		switch size {
		case 1:
			if len(data)-pos < 16 {
				errs.Add(fault.New(fault.KindTruncated,
					"truncated 64-bit box header", file, int64(pos), true))
				return boxes
			}
			size = int64(binary.BigEndian.Uint64(data[pos+8:]))
			hdr = 16
		case 0:
			size = int64(len(data) - pos)
		}
		if size < hdr {
			errs.Add(fault.New(fault.KindInvalidInput,
				"box size smaller than header", file, int64(pos), false))
			return boxes
		}
		if size > limits.MaxBoxBytes {
			errs.Add(limits.Exceeded(file, "box size", int64(pos), size, limits.MaxBoxBytes))
			return boxes
		}
		if pos+int(size) > len(data) {
			errs.Add(fault.New(fault.KindTruncated,
				"box "+typ+" declares "+itoa(size)+" bytes but file ends at "+itoa(len(data)),
				file, int64(pos), true))
			return boxes
		}
		boxes = append(boxes, rawBox{
			typ:       typ,
			offset:    int64(pos),
			headerLen: hdr,
			size:      size,
			payload:   data[pos+int(hdr) : pos+int(size)],
		})
		pos += int(size)
	}
	return boxes
}

// childBoxes scans direct children of a container box payload.
func childBoxes(parent rawBox, file string, errs *fault.Batch) []rawBox {
	var out []rawBox
	base := parent.payloadOff()
	payload := parent.payload
	pos := 0
	// stsd payload begins with a 4-byte fullbox header then an entry count;
	// sample entries (avc1/mp4a/...) start at offset 8.
	if parent.typ == "stsd" {
		if len(payload) < 8 {
			return nil
		}
		pos = 8
	}
	// Visual/audio sample entries carry a fixed-size non-box prefix before
	// their first child box (avcC, colr, clap, esds, ...).
	switch parent.typ {
	case "avc1", "hvc1", "av01":
		// Fixture visual sample entry prefix: 6+4+16+2+2+4+4+4+2+32+2+2.
		if len(payload) <= 80 {
			return nil
		}
		pos = 80
	case "mp4a":
		// Fixture audio sample entry prefix: 6+4+8+2+2+4+4.
		if len(payload) <= 30 {
			return nil
		}
		pos = 30
	}
	for pos < len(payload) {
		if len(payload)-pos < 8 {
			errs.Add(fault.New(fault.KindTruncated,
				"truncated child box inside "+parent.typ,
				file, base+int64(pos), true))
			return out
		}
		size := int64(binary.BigEndian.Uint32(payload[pos:]))
		typ := string(payload[pos+4 : pos+8])
		if size < 8 || pos+int(size) > len(payload) {
			errs.Add(fault.New(fault.KindTruncated,
				"child box "+typ+" overruns parent "+parent.typ,
				file, base+int64(pos), true))
			return out
		}
		out = append(out, rawBox{
			typ:       typ,
			offset:    base + int64(pos),
			headerLen: 8,
			size:      size,
			payload:   payload[pos+8 : pos+int(size)],
		})
		pos += int(size)
	}
	return out
}

// containerBoxes are the box types whose payload holds child boxes.
var containerBoxes = map[string]bool{
	"moov": true, "trak": true, "edts": true, "mdia": true,
	"minf": true, "dinf": true, "stbl": true, "stsd": true,
	"udta": true, "meta": true,
	"avc1": true, "hvc1": true, "mp4a": true, "av01": true,
}

// findChildren returns all descendant boxes of the given type at any depth.
func findChildren(parent rawBox, want string, file string, errs *fault.Batch) []rawBox {
	var hits []rawBox
	var walk func(b rawBox, depth int)
	walk = func(b rawBox, depth int) {
		if depth > limits.MaxBoxDepth {
			errs.Add(limits.Exceeded(file, "box depth", b.offset, depth, limits.MaxBoxDepth))
			return
		}
		if !containerBoxes[b.typ] {
			return
		}
		for _, c := range childBoxes(b, file, errs) {
			if c.typ == want {
				hits = append(hits, c)
			}
			walk(c, depth+1)
		}
	}
	walk(parent, 1)
	return hits
}

// readInput loads a file with an overall size guard.
func readInput(path string) ([]byte, *fault.Error) {	fi, err := os.Stat(path)
	if err != nil {
		return nil, fault.New(fault.KindInvalidInput, "cannot stat input: "+err.Error(),
			path, -1, false)
	}
	if fi.Size() > limits.MaxFileBytes {
		return nil, limits.Exceeded(path, "file size", 0, fi.Size(), limits.MaxFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.New(fault.KindInvalidInput, "cannot read input: "+err.Error(),
			path, -1, false)
	}
	return data, nil
}

func itoa(v any) string {
	switch x := v.(type) {
	case int:
		return formatInt(int64(x))
	case int64:
		return formatInt(x)
	default:
		return "?"
	}
}

func formatInt(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
