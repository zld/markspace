// Package container wraps github.com/abema/go-mp4 to give each produced
// segment an MP4 box structure and a verifiable write/read round trip. The
// box layout is deliberately simple: an ftyp header, a free box carrying the
// segment payload and a nested mrot/mwrp/mdac container whose hierarchy is
// read back to prove structural fidelity.
package container

import (
	"bytes"
	"errors"
	"io"

	mp4 "github.com/abema/go-mp4"

	errs "mediad/internal/errors"
)

// segBox is a registered custom box holding one segment payload.
type segBox struct {
	mp4.AnyTypeBox
	Data []uint8 `mp4:"0,size=8"`
}

// wrapBox and rootBox are pure containers: they declare no payload fields,
// so go-mp4's UnmarshalAny consumes zero bytes and Expand scans their child
// boxes directly from the payload region.
type wrapBox struct {
	mp4.AnyTypeBox
}

type rootBox struct {
	mp4.AnyTypeBox
}

var (
	segType  = mp4.StrToBoxType("mdac")
	wrapType = mp4.StrToBoxType("mwrp")
	rootType = mp4.StrToBoxType("mrot")
)

func init() {
	mp4.AddAnyTypeBoxDef(&segBox{}, segType)
	mp4.AddAnyTypeBoxDef(&wrapBox{}, wrapType)
	mp4.AddAnyTypeBoxDef(&rootBox{}, rootType)
}

// memFile is an in-memory, fully seekable buffer implementing both
// io.WriteSeeker and io.ReadSeeker without touching the filesystem.
type memFile struct {
	buf []byte
	pos int64
}

func (m *memFile) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end > int64(len(m.buf)) {
		m.buf = append(m.buf, make([]byte, end-int64(len(m.buf)))...)
	}
	copy(m.buf[m.pos:end], p)
	m.pos = end
	return len(p), nil
}

func (m *memFile) Read(p []byte) (int, error) {
	if m.pos >= int64(len(m.buf)) {
		return 0, io.EOF
	}
	n := copy(p, m.buf[m.pos:])
	m.pos += int64(n)
	return n, nil
}

func (m *memFile) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = m.pos + offset
	case io.SeekEnd:
		abs = int64(len(m.buf)) + offset
	}
	if abs < 0 {
		return 0, errors.New("negative seek position")
	}
	m.pos = abs
	return abs, nil
}

// BuildSegmentContainer serializes one payload into an MP4 box structure.
// Box headers are written through the go-mp4 Writer, which patches the size
// fields on EndBox; payloads go through the reflective Marshal path.
func BuildSegmentContainer(payload []byte) ([]byte, error) {
	f := &memFile{}
	w := mp4.NewWriter(f)
	brand := [4]byte{'i', 's', 'o', 'm'}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "start ftyp: %v", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:       brand,
		MinorVersion:     1,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: brand}},
	}, mp4.Context{}); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "marshal ftyp: %v", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "end ftyp: %v", err)
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFree()}); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "start free: %v", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Free{Data: append([]byte(nil), payload...)},
		mp4.Context{}); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "marshal free: %v", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "end free: %v", err)
	}

	leaf := &segBox{
		AnyTypeBox: mp4.AnyTypeBox{Type: segType},
		Data:       append([]byte(nil), payload...),
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: rootType}); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "start mrot: %v", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: wrapType}); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "start mwrp: %v", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: segType}); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "start mdac: %v", err)
	}
	if _, err := mp4.Marshal(w, leaf, mp4.Context{}); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "marshal mdac: %v", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "end mdac: %v", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "end mwrp: %v", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, errs.New(errs.Fatal, errs.CodeInternal, "end mrot: %v", err)
	}
	return f.buf, nil
}

// ReadSegmentContainer parses the container and returns the payload held by
// the free box plus the depth at which the custom mdac box was found.
func ReadSegmentContainer(data []byte) (freePayload []byte, depth int, err error) {
	r := bytes.NewReader(data)
	_, err = mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeFree():
			buf := bytes.NewBuffer(nil)
			if _, e := h.ReadData(buf); e != nil {
				return nil, e
			}
			freePayload = buf.Bytes()
		case rootType, wrapType:
			depth++
			return h.Expand()
		case segType:
			depth++
		}
		return nil, nil
	})
	if err != nil {
		return nil, 0, errs.New(errs.Fatal, errs.CodeInternal, "read structure: %v", err)
	}
	return freePayload, depth, nil
}

// VerifyRoundTrip builds then parses a container and reports whether the
// payload survives intact and the nested box hierarchy reaches depth 3.
func VerifyRoundTrip(payload []byte) (bool, error) {
	data, err := BuildSegmentContainer(payload)
	if err != nil {
		return false, err
	}
	got, depth, err := ReadSegmentContainer(data)
	if err != nil {
		return false, err
	}
	return bytes.Equal(got, payload) && depth == 3, nil
}
