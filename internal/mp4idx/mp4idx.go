// Package mp4idx renders and parses the small MP4 box stream that accompanies
// a packaged interval index. It uses github.com/abema/go-mp4 to parse the
// ftyp box structure; the keyframe index itself is carried in a free box as
// versioned JSON. This gives the fan-out/fan-in path a real MP4 fragment/index
// parsing hook without depending on external media files.
package mp4idx

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"

	mp4 "github.com/abema/go-mp4"
)

// BoxMagic identifies the free box carrying our JSON index.
const BoxMagic = "MPID"

// IndexPayload is the JSON stored in the free box.
type IndexPayload struct {
	Magic       string  `json:"magic"`
	IntervalID  string  `json:"interval_id"`
	StartTick   uint64  `json:"start_tick"`
	EndTick     uint64  `json:"end_tick"`
	KeyframeOff []int64 `json:"keyframe_offsets"`
}

// Build renders an ftyp box plus a free box containing the JSON index. The
// box framing is written directly; parsing is delegated to go-mp4.
func Build(p IndexPayload) ([]byte, error) {
	p.Magic = BoxMagic
	var buf bytes.Buffer
	ftypPayload := make([]byte, 0, 12)
	ftypPayload = append(ftypPayload, 'i', 's', 'o', 'm')
	var minor [4]byte
	binary.BigEndian.PutUint32(minor[:], 0x00000200)
	ftypPayload = append(ftypPayload, minor[:]...)
	writeBox(&buf, [4]byte{'f', 't', 'y', 'p'}, ftypPayload)

	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	writeBox(&buf, [4]byte{'f', 'r', 'e', 'e'}, raw)
	return buf.Bytes(), nil
}

func writeBox(buf *bytes.Buffer, typ [4]byte, payload []byte) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(8+len(payload)))
	buf.Write(size[:])
	buf.Write(typ[:])
	buf.Write(payload)
}

// Parse walks the MP4 box structure with go-mp4 and extracts the index payload.
func Parse(data []byte) (*IndexPayload, bool, error) {
	var out IndexPayload
	foundFtyp := false
	foundIndex := false
	_, err := mp4.ReadBoxStructure(bytes.NewReader(data), func(h *mp4.ReadHandle) (any, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeFtyp():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			ft, ok := box.(*mp4.Ftyp)
			if !ok {
				return nil, fmt.Errorf("ftyp payload has unexpected type %T", box)
			}
			if string(ft.MajorBrand[:]) != "isom" {
				return nil, fmt.Errorf("unexpected major brand %q", string(ft.MajorBrand[:]))
			}
			foundFtyp = true
		case mp4.StrToBoxType("free"):
			var body bytes.Buffer
			if _, err := h.ReadData(&body); err != nil {
				return nil, err
			}
			var p IndexPayload
			if err := json.Unmarshal(body.Bytes(), &p); err == nil && p.Magic == BoxMagic {
				out, foundIndex = p, true
			}
		}
		return nil, nil
	})
	if err != nil {
		return nil, foundFtyp, fmt.Errorf("read mp4 boxes: %w", err)
	}
	if !foundFtyp {
		return nil, false, fmt.Errorf("ftyp box not found")
	}
	if !foundIndex {
		return nil, true, fmt.Errorf("index free box not found")
	}
	return &out, true, nil
}
