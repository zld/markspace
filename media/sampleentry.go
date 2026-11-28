package media

import (
	"bytes"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
)

func parseCtts(payload []byte) (map[uint32]int64, error) {
	out := map[uint32]int64{}
	if payload == nil {
		return out, nil
	}
	var ctts mp4.Ctts
	if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), &ctts, mp4.Context{}); err != nil {
		return nil, fmt.Errorf("ctts: %w", err)
	}
	var num uint32
	for _, e := range ctts.Entries {
		var off int64
		if ctts.GetVersion() == 0 {
			off = int64(e.SampleOffsetV0)
		} else {
			off = int64(e.SampleOffsetV1)
		}
		for j := uint32(0); j < e.SampleCount; j++ {
			num++
			out[num] = off
		}
	}
	return out, nil
}

// parseSampleEntry extracts the codec fourcc and configuration box from stsd.
func parseSampleEntry(r *bytes.Reader, stsdPayload []byte, stsd *mp4.Stsd, t *Track) error {
	if stsd.EntryCount == 0 {
		return nil
	}
	// The sample entry starts after the stsd version/flags (4 bytes) and the
	// entry_count (4 bytes).
	if len(stsdPayload) < 8 {
		return fmt.Errorf("stsd payload is %d bytes, too short for an entry", len(stsdPayload))
	}
	body := stsdPayload[8:]
	entry, err := readBoxHeader(bytes.NewReader(body), 0, uint64(len(body)))
	if err != nil {
		return fmt.Errorf("stsd sample entry: %w", err)
	}
	t.SampleEntry = string(entry.typ[:])
	// Codec configuration boxes live as children of the sample entry; parse
	// them from the raw entry bytes.
	if entry.payloadSize() >= 8 {
		sub, serr := childBoxes(bytes.NewReader(body), entry, "avcC")
		if serr == nil && len(sub) > 0 {
			t.CodecPrivate = payloadCopy(body, sub[0])
		}
	}
	return nil
}

func payloadCopy(buf []byte, b boxSpan) []byte {
	start, end := b.payloadStart(), b.end()
	if start > uint64(len(buf)) || end > uint64(len(buf)) {
		return nil
	}
	return append([]byte(nil), buf[start:end]...)
}

// readChildPayload returns the payload of the first child of parent with the
// given type.
func readChildPayload(r *bytes.Reader, parent boxSpan, typ string) ([]byte, bool, error) {
	kids, err := childBoxes(r, parent, typ)
	if err != nil {
		return nil, false, err
	}
	if len(kids) == 0 {
		return nil, false, nil
	}
	b := kids[0]
	buf := make([]byte, b.payloadSize())
	if _, err := r.Seek(int64(b.payloadStart()), io.SeekStart); err != nil {
		return nil, false, err
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, false, err
	}
	return buf, true, nil
}

func allChildren(r *bytes.Reader, parent boxSpan) ([]boxSpan, error) {
	total := parent.end()
	var out []boxSpan
	for off := parent.payloadStart(); off < total; {
		b, err := readBoxHeader(r, off, total)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
		off = b.end()
	}
	return out, nil
}

func mustChildren(r *bytes.Reader, parent boxSpan) []boxSpan {
	out, _ := allChildren(r, parent)
	return out
}
