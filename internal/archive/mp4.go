package archive

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/abema/go-mp4"

	"media-archiver/internal/media"
)

// WriteManifestArchive stores manifest bytes as an ISOBMFF free box after ftyp.
// go-mp4 is used to parse and verify the encapsulated manifest on read-back.
func WriteManifestArchive(path string, manifest []byte) error {
	var buf []byte
	buf = append(buf, ftypBox()...)
	buf = append(buf, freeBox(manifest)...)
	return media.WriteFileAtomic(path, buf, 0o644)
}

// ReadManifestArchive extracts the free-box manifest payload using go-mp4.
func ReadManifestArchive(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	boxes, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{mp4.BoxTypeFree()})
	if err != nil {
		return nil, media.NewError(media.CodeInternal, media.StageVerify, -1, "parse MP4 manifest archive", err)
	}
	if len(boxes) == 0 {
		return nil, media.NewError(media.CodeInternal, media.StageVerify, -1, "manifest free box missing", nil)
	}
	free, ok := boxes[0].Payload.(*mp4.Free)
	if !ok {
		return nil, media.NewError(media.CodeInternal, media.StageVerify, -1,
			fmt.Sprintf("unexpected free payload type %T", boxes[0].Payload), nil)
	}
	return append([]byte(nil), free.Data...), nil
}

func ftypBox() []byte {
	payload := make([]byte, 0, 12)
	payload = append(payload, []byte("isom")...)
	minor := make([]byte, 4)
	binary.BigEndian.PutUint32(minor, 0x00000200)
	payload = append(payload, minor...)
	payload = append(payload, []byte("isom")...)
	return box("ftyp", payload)
}

func freeBox(payload []byte) []byte { return box("free", payload) }

func box(kind string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], kind)
	copy(out[8:], payload)
	return out
}
