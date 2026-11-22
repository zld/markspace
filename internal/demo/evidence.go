package demo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"github.com/abema/go-mp4"
)

// WriteEvidenceMP4 serializes a minimal ISO-BMFF container that carries the
// run manifest inside a 'free' box. Real deployments place media samples here;
// this kernel uses the box structure as a tamper-evident, parser-verifiable
// envelope without any system codec dependency.
func WriteEvidenceMP4(w io.WriteSeeker, payload []byte) error {
	mw := mp4.NewWriter(w)
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	brand := [4]byte{'m', 'l', 'a', 'b'}
	ft := &mp4.Ftyp{
		MajorBrand:   brand,
		MinorVersion: 1,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: brand},
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	}
	if _, err := mp4.Marshal(mw, ft, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}

	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFree()}); err != nil {
		return err
	}
	// go-mp4 models 'free' payload as 8-bit elements and validates bit
	// alignment on read, so pad to an 8-byte boundary. The original length is
	// encoded as a big-endian uint32 prefix and restored on parse.
	stored := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(stored, uint32(len(payload)))
	copy(stored[4:], payload)
	if pad := len(stored) % 8; pad != 0 {
		stored = append(stored, make([]byte, 8-pad)...)
	}
	if _, err := mp4.Marshal(mw, &mp4.Free{Data: stored},
		mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	return nil
}

// ReadEvidenceMP4 parses the container back and returns the free-box payload,
// proving the written artifact is a structurally valid MP4 file.
func ReadEvidenceMP4(r io.ReadSeeker) (ftypOK bool, payload []byte, err error) {
	_, err = mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeFtyp():
			ftypOK = true
			return nil, nil
		case mp4.BoxTypeFree():
			var free mp4.Free
			if _, perr := mp4.Unmarshal(r, h.BoxInfo.Size-h.BoxInfo.HeaderSize,
				&free, mp4.Context{}); perr != nil {
				return nil, perr
			}
			if len(free.Data) < 4 {
				return nil, errors.New("evidence free box too short")
			}
			n := binary.BigEndian.Uint32(free.Data[:4])
			if int(n) > len(free.Data)-4 {
				return nil, errors.New("evidence length exceeds box payload")
			}
			payload = append([]byte(nil), free.Data[4:4+n]...)
			return nil, nil
		}
		return nil, nil
	})
	return ftypOK, payload, err
}

// EvidenceRoundTrip writes then parses the MP4 and reports whether the
// payload survived intact.
func EvidenceRoundTrip(payload []byte) (bool, error) {
	var buf evidenceBuffer
	if err := WriteEvidenceMP4(&buf, payload); err != nil {
		return false, err
	}
	ok, got, err := ReadEvidenceMP4(bytes.NewReader(buf.Bytes()))
	if err != nil {
		return false, err
	}
	return ok && bytes.Equal(got, payload), nil
}

// evidenceBuffer is an in-memory io.WriteSeeker. EndBox seeks backwards to
// overwrite each box header with its final size, so Seek must move an
// absolute offset into an already-written byte slice rather than tracking a
// separate counter.
type evidenceBuffer struct {
	data []byte
	off  int64
}

func (b *evidenceBuffer) Write(p []byte) (int, error) {
	end := int(b.off) + len(p)
	if cap(b.data) < end {
		grown := make([]byte, end)
		copy(grown, b.data)
		b.data = grown
	} else if len(b.data) < end {
		b.data = b.data[:end]
	}
	copy(b.data[b.off:end], p)
	b.off = int64(end)
	return len(p), nil
}

func (b *evidenceBuffer) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = b.off + offset
	case io.SeekEnd:
		abs = int64(len(b.data)) + offset
	}
	if abs < 0 {
		return 0, errors.New("negative seek offset")
	}
	b.off = abs
	return abs, nil
}

func (b *evidenceBuffer) Bytes() []byte { return b.data }
