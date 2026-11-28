package mp4

import (
	"bytes"
	"encoding/binary"

	gomp4 "github.com/abema/go-mp4"
)

// patchStco rewrites the stco entry offsets in data so each chunk points at
// payloadStart + its relative offset inside the mdat payload.
func patchStco(data []byte, relOffsets []uint64, payloadStart int64) error {
	r := bytes.NewReader(data)
	_, err := gomp4.ReadBoxStructure(r, func(h *gomp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type != gomp4.BoxTypeStco() {
			return h.Expand()
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		stco := box.(*gomp4.Stco)
		payloadOff, err := h.BoxInfo.SeekToPayload(r)
		if err != nil {
			return nil, err
		}
		// stco payload: version/flags (4) + entry_count (4) + entries.
		entryBase := payloadOff + 8
		for i := 0; i < len(stco.ChunkOffset) && i < len(relOffsets); i++ {
			abs := uint32(int64(relOffsets[i]) + payloadStart)
			binary.BigEndian.PutUint32(data[int(entryBase)+4*i:], abs)
		}
		return nil, nil
	})
	return err
}
