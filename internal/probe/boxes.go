// Package probe parses MP4/MOV containers into the unified timeline and
// track graph defined by internal/model. Parsing is structural only: no
// media samples are decoded.
package probe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/abema/go-mp4"
	"github.com/broadcast/mediad/internal/model"
)

// RawBox is a minimal structural descriptor produced by the safety scan
// before the go-mp4 unmarshaller is trusted with the file.
type RawBox struct {
	Type       string
	Offset     int64
	HeaderSize int64
	Size       int64 // declared total size, -1 when the box extends to EOF
	Payload    int64 // payload size in bytes
}

// End returns the absolute offset just past the box.
func (b RawBox) End() int64 {
	if b.Size < 0 {
		return b.Offset + b.HeaderSize + b.Payload
	}
	return b.Offset + b.Size
}

// ScanResult holds the structural scan outcome plus the byte length used to
// detect truncated trailing boxes.
type ScanResult struct {
	Boxes    []RawBox
	FileSize int64
}

// SafePath rejects non-local or traversing paths before any file handle is
// opened. Only plain local files inside the given base directory are allowed.
func SafePath(base, name string) (string, error) {
	if name == "" {
		return "", errors.New("empty path")
	}
	if filepath.IsAbs(name) {
		return "", &PathError{Path: name, Reason: "absolute paths are not allowed"}
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", &PathError{Path: name, Reason: "path traversal segment"}
	}
	joined := filepath.Join(base, clean)
	rel, err := filepath.Rel(base, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", &PathError{Path: name, Reason: "escapes base directory"}
	}
	return joined, nil
}

// PathError is returned for rejected input paths.
type PathError struct {
	Path   string
	Reason string
}

func (e *PathError) Error() string {
	return "rejected path " + e.Path + ": " + e.Reason
}

// OpenFile enforces the delivery size limit before reading any box.
func OpenFile(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if st.Size() > model.MaxFileSize {
		f.Close()
		return nil, st.Size(), &model.LimitError{What: "file size", Got: int(st.Size()), Max: int(model.MaxFileSize)}
	}
	return f, st.Size(), nil
}

// ScanBoxes walks the box tree non-recursively for the requested depth and
// validates every declared length against the actual file bounds. A box that
// runs past EOF is recorded with Truncated=true semantics via the returned
// error set instead of panicking inside the generic unmarshaller.
func ScanBoxes(r io.ReadSeeker, fileSize int64, errs *model.ErrorSet) (*ScanResult, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	res := &ScanResult{FileSize: fileSize}
	pos := int64(0)
	for pos < fileSize {
		box, next, err := readOneBoxHeader(r, pos, fileSize)
		if err != nil {
			errs.Add(model.MediaError{
				Kind:        model.KindTruncatedBox,
				SampleIndex: -1,
				Detail:      err.Error(),
			})
			// A truncated trailing box ends the walk; earlier boxes stay valid.
			return res, nil
		}
		res.Boxes = append(res.Boxes, box)
		pos = next
	}
	return res, nil
}

func readOneBoxHeader(r io.ReadSeeker, pos, fileSize int64) (RawBox, int64, error) {
	hdr := make([]byte, 8)
	if _, err := readFullAt(r, hdr, pos); err != nil {
		return RawBox{}, 0, fmt.Errorf("box header at offset %d is unreadable: %w", pos, err)
	}
	size32 := int64(binary.BigEndian.Uint32(hdr[:4]))
	typ := string(bytes.TrimRight(hdr[4:8], "\x00"))
	box := RawBox{Type: typ, Offset: pos, HeaderSize: 8, Size: size32}

	switch size32 {
	case 1: // 64-bit largesize
		ext := make([]byte, 8)
		if _, err := readFullAt(r, ext, pos+8); err != nil {
			return RawBox{}, 0, fmt.Errorf("box %q largesize is truncated", typ)
		}
		large := int64(binary.BigEndian.Uint64(ext))
		if large < 16 {
			return RawBox{}, 0, fmt.Errorf("box %q has invalid largesize %d", typ, large)
		}
		box.HeaderSize = 16
		box.Size = large
	case 0: // box extends to EOF
		box.Size = -1
		box.Payload = fileSize - pos - 8
		if box.Payload < 0 {
			return RawBox{}, 0, fmt.Errorf("box %q extends past EOF", typ)
		}
		return box, fileSize, nil
	default:
		if size32 < 8 {
			return RawBox{}, 0, fmt.Errorf("box %q has invalid size %d", typ, size32)
		}
	}

	box.Payload = box.Size - box.HeaderSize
	if box.Payload < 0 || pos+box.Size > fileSize {
		return box, 0, fmt.Errorf("box %q declares %d bytes but only %d remain",
			typ, box.Size, fileSize-pos)
	}
	return box, pos + box.Size, nil
}

func readFullAt(r io.ReadSeeker, buf []byte, off int64) (int, error) {
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(r, buf)
}

// FindBox returns the first top-level box of the given type, if present.
func (s *ScanResult) FindBox(typ string) (RawBox, bool) {
	for _, b := range s.Boxes {
		if b.Type == typ {
			return b, true
		}
	}
	return RawBox{}, false
}

// BoxTypeString is a small helper so callers do not import go-mp4 just for
// fourcc conversion.
func BoxTypeString(t mp4.BoxType) string { return t.String() }
