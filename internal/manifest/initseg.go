package manifest

import (
	"bytes"
	"encoding/binary"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// WriteInitSegment writes a minimal ISO-BMFF init segment (ftyp) that
// accompanies the manifest, identifying the stream as H.264/AVC.
func WriteInitSegment(w io.Writer) error {
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 512,
	}
	ftyp.AddCompatibleBrand([4]byte{'i', 's', 'o', 'm'})
	ftyp.AddCompatibleBrand([4]byte{'a', 'v', 'c', '1'})
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, ftyp, mp4.Context{}); err != nil {
		return err
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(8+payload.Len()))
	copy(hdr[4:], "ftyp")
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload.Bytes())
	return err
}
