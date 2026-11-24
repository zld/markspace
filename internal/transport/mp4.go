package transport

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/abema/go-mp4"

	"archive-recovery/internal/media"
)

// Box type 'arcv' carries the serialized provenance manifest inside an
// otherwise minimal MP4-like container.
var boxTypeARCV = mp4.StrToBoxType("arcv")

// WriteMP4Container serializes the manifest bytes into a minimal MP4 file
// (ftyp + arcv). The ftyp box is produced through the pinned go-mp4
// marshaller; the custom arcv box uses explicit length-prefixed framing so
// no dynamic-field reflection hooks are required.
func WriteMP4Container(manifest []byte) ([]byte, error) {
	var buf bytes.Buffer
	ftyp := &mp4.Ftyp{
		MajorBrand:   mp4.StrToBoxType("arcv"),
		MinorVersion: 1,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: mp4.StrToBoxType("isom")},
			{CompatibleBrand: mp4.StrToBoxType("arcv")},
		},
	}
	// Marshal the payload first to learn its size, then WriteBoxInfo emits
	// the canonical 8-byte box header around it.
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, ftyp, mp4.Context{}); err != nil {
		return nil, media.WrapError(media.ClassFatal, "transport", "ftyp marshal", err)
	}
	info := &mp4.BoxInfo{
		Offset: 0, Size: uint64(8 + payload.Len()), HeaderSize: 8,
		Type: mp4.BoxTypeFtyp(), Context: mp4.Context{},
	}
	ws := &writeSeeker{b: &buf}
	if _, err := mp4.WriteBoxInfo(ws, info); err != nil {
		return nil, media.WrapError(media.ClassFatal, "transport", "ftyp header", err)
	}
	buf.Write(payload.Bytes())

	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(8+len(manifest)))
	copy(hdr[4:8], boxTypeARCV[:])
	buf.Write(hdr[:])
	buf.Write(manifest)
	return buf.Bytes(), nil
}

// writeSeeker adapts bytes.Buffer to io.WriteSeeker. WriteBoxInfo only
// queries the current position and appends, so a forward cursor suffices.
type writeSeeker struct {
	b   *bytes.Buffer
	pos int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	n, err := w.b.Write(p)
	w.pos += int64(n)
	return n, err
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		if offset == 0 {
			return w.pos, nil
		}
	case io.SeekStart:
		if offset == w.pos {
			return w.pos, nil
		}
	}
	return 0, media.NewError(media.ClassFatal, "transport", "unsupported seek")
}

// ReadMP4Container parses the minimal container back via go-mp4's box
// reader and returns the arcv payload, proving the round trip preserves
// the manifest byte-for-byte.
func ReadMP4Container(container []byte) ([]byte, error) {
	r := bytes.NewReader(container)
	for {
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			break
		}
		if bi.Type != boxTypeARCV {
			if _, err := bi.SeekToEnd(r); err != nil {
				break
			}
			continue
		}
		payloadSize := bi.Size - bi.HeaderSize
		if payloadSize > uint64(len(container)) {
			return nil, media.NewError(media.ClassRecoverable, "transport",
				"arcv length field overflow")
		}
		payload := make([]byte, payloadSize)
		if _, err := r.Read(payload); err != nil {
			return nil, media.WrapError(media.ClassRecoverable, "transport",
				"arcv payload read", err)
		}
		return payload, nil
	}
	return nil, media.NewError(media.ClassRecoverable, "transport", "arcv box missing")
}
