// Package mp4 scans fragmented MP4 containers with strict byte bounds. It
// reports the exact input offset of every truncated, oversized or malformed
// box instead of trusting lengths found in the file, and it hands payloads
// to go-mp4 for structural decoding.
package mp4

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"replaykit/media/fault"
)

// Limits bounds memory and rejects hostile or corrupt input early.
type Limits struct {
	// MaxFileBytes is the largest input accepted at all.
	MaxFileBytes int64 `json:"max_file_bytes"`
	// MaxBoxBytes is the largest single box payload accepted.
	MaxBoxBytes int64 `json:"max_box_bytes"`
}

// DefaultLimits are deliberately modest for a terminal tool.
func DefaultLimits() Limits {
	return Limits{MaxFileBytes: 64 << 20, MaxBoxBytes: 8 << 20}
}

// Validate rejects limits that would disable the guards.
func (l Limits) Validate() error {
	if l.MaxFileBytes <= 0 || l.MaxBoxBytes <= 0 {
		return fmt.Errorf("mp4: limits must be positive (file=%d box=%d)", l.MaxFileBytes, l.MaxBoxBytes)
	}
	if l.MaxBoxBytes > l.MaxFileBytes {
		return fmt.Errorf("mp4: box limit %d exceeds file limit %d", l.MaxBoxBytes, l.MaxFileBytes)
	}
	return nil
}

// BoxRef identifies one box in the input.
type BoxRef struct {
	Type       string `json:"type"`
	Offset     int64  `json:"offset"`
	Size       int64  `json:"size"`
	HeaderSize int64  `json:"header_size"`
	Path       string `json:"path"`
}

// PayloadOffset is the first byte after the box header.
func (b BoxRef) PayloadOffset() int64 { return b.Offset + b.HeaderSize }

// PayloadSize is the number of payload bytes.
func (b BoxRef) PayloadSize() int64 { return b.Size - b.HeaderSize }

// End is the first byte after the box.
func (b BoxRef) End() int64 { return b.Offset + b.Size }

// Scan is the result of walking one container file.
type Scan struct {
	FileSize  int64           `json:"file_size"`
	Boxes     []BoxRef        `json:"boxes"`
	TopLevel  []BoxRef        `json:"top_level"`
	Anomalies []fault.Anomaly `json:"anomalies"`
	Fatal     error           `json:"-"`
}

// Find returns every box of the given type.
func (s *Scan) Find(boxType string) []BoxRef {
	var out []BoxRef
	for _, b := range s.Boxes {
		if b.Type == boxType {
			out = append(out, b)
		}
	}
	return out
}

// First returns the first box of the given type.
func (s *Scan) First(boxType string) (BoxRef, bool) {
	for _, b := range s.Boxes {
		if b.Type == boxType {
			return b, true
		}
	}
	return BoxRef{}, false
}

// ScanFile reads and scans a file under limits. A file larger than the limit
// is a fatal input error; structural defects are recorded as anomalies.
func ScanFile(path string, limits Limits) (*Scan, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.Fatal("open_mp4", "", 0, fault.Range{}, err)
	}
	defer f.Close()
	// Stat first so a huge file is rejected without reading it.
	info, err := f.Stat()
	if err != nil {
		return nil, fault.Fatal("stat_mp4", "", 0, fault.Range{}, err)
	}
	if info.Size() > limits.MaxFileBytes {
		return nil, fault.Fatal("read_mp4", "", 0, fault.Range{},
			fmt.Errorf("file %q is %d bytes, above limit %d", path, info.Size(), limits.MaxFileBytes))
	}
	data, err := io.ReadAll(io.LimitReader(f, limits.MaxFileBytes+1))
	if err != nil {
		return nil, fault.Fatal("read_mp4", "", 0, fault.Range{}, err)
	}
	return ScanBytes(data, limits)
}

// ScanBytes scans an in-memory container. Structural defects are recorded
// as anomalies and the walk stops at the first untrusted length. A scan that
// finds no parseable box at all is fatal, and the error names the offset
// where the walk stopped so the failure is traceable to a byte.
func ScanBytes(data []byte, limits Limits) (*Scan, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if int64(len(data)) > limits.MaxFileBytes {
		return nil, fault.Fatal("scan_mp4", "", 0, fault.Range{},
			fmt.Errorf("input is %d bytes, above limit %d", len(data), limits.MaxFileBytes))
	}
	w := &walker{data: data, limits: limits, scan: &Scan{FileSize: int64(len(data))}}
	w.walk(0, int64(len(data)), "", true)
	if len(w.scan.Boxes) == 0 {
		off := int64(0)
		detail := "no MP4 boxes found"
		if n := len(w.scan.Anomalies); n > 0 {
			off = w.scan.Anomalies[0].Offset
			detail = w.scan.Anomalies[0].Detail
		}
		w.scan.Fatal = fault.Fatal("scan_mp4", "", off, fault.Range{}, fmt.Errorf("%s", detail))
		return w.scan, w.scan.Fatal
	}
	return w.scan, nil
}

type walker struct {
	data   []byte
	limits Limits
	scan   *Scan
}

func (w *walker) addAnomaly(off int64, cat fault.Category, sev fault.Severity, act fault.Action, detail string) {
	w.scan.Anomalies = append(w.scan.Anomalies, fault.New("", off, cat, sev, act, 0, fault.Range{}, detail))
}

// walk iterates boxes in [start,end). It stops at the first unrecoverable
// boundary because no later offset can be trusted after a bad length.
func (w *walker) walk(start, end int64, path string, top bool) {
	off := start
	for off < end {
		if end-off < 8 {
			w.addAnomaly(off, fault.CategoryTruncatedBox, fault.SeverityError, fault.ActionSkipped,
				fmt.Sprintf("%d trailing bytes cannot form a box header", end-off))
			return
		}
		size := int64(binary.BigEndian.Uint32(w.data[off:]))
		typ := string(w.data[off+4 : off+8])
		header := int64(8)
		bad := false
		switch size {
		case 0:
			// Extends to the end of this container.
			size = end - off
		case 1:
			if end-off < 16 {
				w.addAnomaly(off, fault.CategoryTruncatedBox, fault.SeverityError, fault.ActionSkipped,
					fmt.Sprintf("large-size header for %q truncated", typ))
				return
			}
			size = int64(binary.BigEndian.Uint64(w.data[off+8:]))
			header = 16
		}
		if size < header {
			w.addAnomaly(off, fault.CategoryMalformedBox, fault.SeverityError, fault.ActionSkipped,
				fmt.Sprintf("box %q declares size %d below header %d", typ, size, header))
			bad = true
		} else if size > w.limits.MaxBoxBytes {
			w.addAnomaly(off, fault.CategoryBoxOverflow, fault.SeverityError, fault.ActionSkipped,
				fmt.Sprintf("box %q size %d exceeds limit %d", typ, size, w.limits.MaxBoxBytes))
			bad = true
		} else if off+size > end {
			w.addAnomaly(off, fault.CategoryTruncatedBox, fault.SeverityError, fault.ActionSkipped,
				fmt.Sprintf("box %q needs %d bytes at offset %d but only %d remain", typ, size, off, end-off))
			bad = true
		}
		if bad {
			return
		}
		ref := BoxRef{Type: typ, Offset: off, Size: size, HeaderSize: header, Path: joinPath(path, typ)}
		w.scan.Boxes = append(w.scan.Boxes, ref)
		if top {
			w.scan.TopLevel = append(w.scan.TopLevel, ref)
		}
		if isContainer(typ) {
			w.walk(ref.PayloadOffset(), ref.End(), ref.Path, false)
		}
		off += size
	}
}

func joinPath(parent, typ string) string {
	if parent == "" {
		return typ
	}
	return parent + "/" + typ
}

func isContainer(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "mvex", "moof", "traf",
		"edts", "dinf", "udta", "mfra", "strk", "stri":
		return true
	}
	return false
}

// Payload returns the payload bytes of a box, bounded by the scan limits.
func (s *Scan) Payload(data []byte, ref BoxRef) ([]byte, error) {
	if ref.Offset < 0 || ref.Size < ref.HeaderSize || ref.End() > int64(len(data)) {
		return nil, fmt.Errorf("mp4: box %q [%d,%d) outside input of %d bytes", ref.Type, ref.Offset, ref.End(), len(data))
	}
	return data[ref.PayloadOffset():ref.End()], nil
}

// Unmarshal decodes a box payload with go-mp4. A panic from a malformed
// payload is converted into an error so one bad box cannot abort the scan.
func Unmarshal(payload []byte, dst gomp4.IBox) (n uint64, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("mp4: decoder panic: %v", r)
		}
	}()
	return gomp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), dst, gomp4.Context{})
}

// Decode is a generic helper that unmarshals payload into a fresh box.
func Decode[T gomp4.IBox](payload []byte, dst T) (T, error) {
	_, err := Unmarshal(payload, dst)
	return dst, err
}

// BoxTypeOf converts a four-character code for comparisons in tests/tools.
func BoxTypeOf(code string) gomp4.BoxType { return gomp4.StrToBoxType(code) }
