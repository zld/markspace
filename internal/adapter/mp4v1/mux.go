package mp4v1

import (
	"bytes"
	"encoding/binary"
	"math"

	gomp4 "github.com/abema/go-mp4"
)

// EncodeSegment wraps a deterministic medpipe segment payload in a small
// ISO-BMFF file: ftyp followed by an mdat box. The payload itself is produced
// by the pipeline and covered by the manifest checksum.
func EncodeSegment(payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	ftyp := gomp4.Ftyp{
		MajorBrand:   gomp4.BrandISOM(),
		MinorVersion: 0x00000200,
	}
	ftyp.AddCompatibleBrand(gomp4.BrandMP41())
	if _, err := gomp4.Marshal(&buf, &ftyp, gomp4.Context{}); err != nil {
		return nil, err
	}

	if uint64(len(payload))+8 <= math.MaxUint32 {
		var header [8]byte
		binary.BigEndian.PutUint32(header[0:4], uint32(len(payload)+8))
		copy(header[4:8], "mdat")
		buf.Write(header[:])
	} else {
		var header [16]byte
		binary.BigEndian.PutUint32(header[0:4], 1)
		copy(header[4:8], "mdat")
		binary.BigEndian.PutUint64(header[8:16], uint64(len(payload))+16)
		buf.Write(header[:])
	}
	buf.Write(payload)
	return buf.Bytes(), nil
}
