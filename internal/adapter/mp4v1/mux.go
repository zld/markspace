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
	var ftypPayload bytes.Buffer
	ftyp := gomp4.Ftyp{
		MajorBrand:   gomp4.BrandISOM(),
		MinorVersion: 0x00000200,
	}
	ftyp.AddCompatibleBrand(gomp4.BrandMP41())
	if _, err := gomp4.Marshal(&ftypPayload, &ftyp, gomp4.Context{}); err != nil {
		return nil, err
	}

	var out bytes.Buffer
	if err := writeBoxHeader(&out, "ftyp", uint64(ftypPayload.Len())); err != nil {
		return nil, err
	}
	out.Write(ftypPayload.Bytes())
	if err := writeBoxHeader(&out, "mdat", uint64(len(payload))); err != nil {
		return nil, err
	}
	out.Write(payload)
	return out.Bytes(), nil
}

func writeBoxHeader(buf *bytes.Buffer, boxType string, payloadSize uint64) error {
	size := payloadSize + 8
	if size <= math.MaxUint32 {
		var header [8]byte
		binary.BigEndian.PutUint32(header[0:4], uint32(size))
		copy(header[4:8], boxType)
		_, err := buf.Write(header[:])
		return err
	}
	var header [16]byte
	binary.BigEndian.PutUint32(header[0:4], 1)
	copy(header[4:8], boxType)
	binary.BigEndian.PutUint64(header[8:16], payloadSize+16)
	_, err := buf.Write(header[:])
	return err
}
