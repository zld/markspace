// Package index builds a random-access index over verified keyframes. Only
// packets that carry both a random-access indicator and a PES unit-start are
// admitted, so seeking an index entry always reads back the same frame. The
// index is serialized into a minimal MP4-compatible container via go-mp4.
package index

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"github.com/example/tsrepair/internal/model"
	"github.com/example/tsrepair/internal/ts"
)

// Keyframe is one verifiable random-access point.
type Keyframe struct {
	ID           int
	PacketOffset int64 // ordinal of the TS packet in the input stream
	ByteOffset   int64 // byte offset of the packet's sync byte
	PTS          uint64 // unwrapped 90 kHz PTS
	PayloadLen   int
}

// Index is the ordered collection of verified keyframes.
type Index struct {
	Frames []Keyframe
}

// AddResult reports whether a candidate was admitted and why when rejected.
type AddResult struct {
	Admitted bool
	Reason   string
}

// Builder admits only verifiable random-access packets.
type Builder struct {
	idx Index
}

// NewBuilder returns an empty index builder.
func NewBuilder() *Builder { return &Builder{} }

// Consider evaluates one packet. A candidate must (1) start a PES unit,
// (2) carry the adaptation-field random-access indicator, and (3) have a
// non-empty payload. Sparse streams and truncated tails simply yield fewer or
// no frames rather than unsafe entries.
func (b *Builder) Consider(p *ts.Packet, packetOrdinal int64, pts uint64) AddResult {
	switch {
	case !p.PayloadUnitStart:
		return AddResult{false, "not-unit-start"}
	case p.Adaptation == nil || !p.Adaptation.RandomAccess:
		return AddResult{false, "no-random-access-indicator"}
	case len(p.Payload) == 0:
		return AddResult{false, "empty-payload"}
	}
	b.idx.Frames = append(b.idx.Frames, Keyframe{
		ID:           len(b.idx.Frames),
		PacketOffset: packetOrdinal,
		ByteOffset:   p.ByteOffset,
		PTS:          pts,
		PayloadLen:   len(p.Payload),
	})
	return AddResult{true, ""}
}

// Build returns the accumulated index.
func (b *Builder) Build() *Index {
	out := Index{Frames: append([]Keyframe(nil), b.idx.Frames...)}
	return &out
}

// Lookup returns the keyframe whose byte offset is at or before target and
// closest to it. The boolean is false when no such frame exists (sparse
// head), which callers report as an explicit seek miss.
func (ix *Index) Lookup(target int64) (Keyframe, bool) {
	var best Keyframe
	found := false
	for _, f := range ix.Frames {
		if f.ByteOffset <= target {
			best, found = f, true
		} else {
			break
		}
	}
	return best, found
}

// VerifySeek re-reads the indexed packet from data and confirms it still
// starts at the recorded byte offset with sync byte, PUSI and the random
// access indicator intact. This is the read-back proof that the index points
// at the same frame that was originally recorded.
func (ix *Index) VerifySeek(f Keyframe, data []byte) error {
	end := f.ByteOffset + ts.PacketSize
	if f.ByteOffset < 0 || int(end) > len(data) {
		return model.New(model.StageIndex, model.SeverityRecoverable,
			"indexed frame lies beyond available data (truncated tail)").
			At(model.PIDNotApplicable, f.ByteOffset)
	}
	pkt, err := ts.Parse(data[f.ByteOffset:end], f.ByteOffset)
	if err != nil {
		return err
	}
	if !pkt.PayloadUnitStart || pkt.Adaptation == nil || !pkt.Adaptation.RandomAccess {
		return model.New(model.StageIndex, model.SeverityFatal,
			"read-back packet is no longer a verifiable keyframe").
			At(pkt.PID, f.ByteOffset)
	}
	if pkt.ByteOffset != f.ByteOffset {
		return model.New(model.StageIndex, model.SeverityInternal,
			fmt.Sprintf("offset mismatch: index=%d packet=%d", f.ByteOffset, pkt.ByteOffset))
	}
	return nil
}

// indexBoxType names the custom 'kidx' box serialized under the standard
// 'udta' container; its payload layout is defined by encodeIndexPayload.
const indexBoxType = "kidx"

// SerializeMP4 writes the index into an MP4-compatible byte stream: a
// standard ftyp box followed by udta/kidx holding fixed 28-byte keyframe
// records. go-mp4 owns every box header and size field.
func (ix *Index) SerializeMP4() ([]byte, error) {
	ms := &memSeeker{}
	w := mp4.NewWriter(ms)
	// ftyp: StartBox frames the header, Marshal writes the payload, EndBox
	// back-fills the size.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return nil, err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	}, mp4.Context{}); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	// udta container with the custom kidx child.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUdta()}); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(indexBoxType)}); err != nil {
		return nil, err
	}
	if _, err := w.Write(encodeIndexPayload(ix)); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, err
	}
	return ms.data, nil
}

// memSeeker is a growable in-memory WriteSeeker so go-mp4's EndBox can seek
// backwards and back-fill box sizes.
type memSeeker struct {
	data []byte
	pos  int64
}

func (m *memSeeker) Write(p []byte) (int, error) {
	if end := int(m.pos) + len(p); end > len(m.data) {
		m.data = append(m.data, make([]byte, end-len(m.data))...)
	}
	copy(m.data[int(m.pos):], p)
	m.pos += int64(len(p))
	return len(p), nil
}

func (m *memSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		m.pos = offset
	case io.SeekCurrent:
		m.pos += offset
	case io.SeekEnd:
		m.pos = int64(len(m.data)) + offset
	}
	return m.pos, nil
}

func encodeIndexPayload(ix *Index) []byte {
	payload := make([]byte, 4+28*len(ix.Frames))
	binary.BigEndian.PutUint32(payload, uint32(len(ix.Frames)))
	for i, f := range ix.Frames {
		rec := payload[4+i*28:]
		binary.BigEndian.PutUint32(rec[0:4], uint32(f.ID))
		binary.BigEndian.PutUint64(rec[4:12], uint64(f.PacketOffset))
		binary.BigEndian.PutUint64(rec[12:20], uint64(f.ByteOffset))
		binary.BigEndian.PutUint64(rec[20:28], f.PTS)
	}
	return payload
}

// DeserializeMP4 reads back an index produced by SerializeMP4. The custom
// kidx box is not a registered go-mp4 type, so we descend into the standard
// udta container manually and parse kidx with ReadBoxInfo.
func DeserializeMP4(data []byte) (*Index, error) {
	r := bytes.NewReader(data)
	var rawPayload []byte
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type == mp4.BoxTypeUdta() {
			// Manually walk udta's children to find the custom kidx box.
			info, err := mp4.ReadBoxInfo(r)
			if err != nil {
				return nil, err
			}
			if info.Type.String() != indexBoxType {
				return nil, model.New(model.StageIndex, model.SeverityFatal,
					"expected kidx inside udta, got "+info.Type.String())
			}
			var p bytes.Buffer
			if _, err := info.SeekToPayload(r); err != nil {
				return nil, err
			}
			if _, err := io.CopyN(&p, r, int64(info.Size-info.HeaderSize)); err != nil {
				return nil, err
			}
			rawPayload = p.Bytes()
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return nil, model.New(model.StageIndex, model.SeverityFatal,
			"index container unreadable").Wrap(err)
	}
	if rawPayload == nil {
		return nil, model.New(model.StageIndex, model.SeverityFatal,
			"kidx box missing from index container")
	}
	return decodeIndexPayload(rawPayload)
}

func decodeIndexPayload(payload []byte) (*Index, error) {
	if len(payload) < 4 {
		return nil, model.New(model.StageIndex, model.SeverityFatal,
			"kidx payload shorter than count field")
	}
	count := binary.BigEndian.Uint32(payload)
	if len(payload) != 4+int(count)*28 {
		return nil, model.New(model.StageIndex, model.SeverityFatal,
			fmt.Sprintf("kidx record count %d disagrees with payload length %d",
				count, len(payload)))
	}
	ix := &Index{Frames: make([]Keyframe, count)}
	for i := range ix.Frames {
		rec := payload[4+i*28:]
		ix.Frames[i] = Keyframe{
			ID:           int(binary.BigEndian.Uint32(rec[0:4])),
			PacketOffset: int64(binary.BigEndian.Uint64(rec[4:12])),
			ByteOffset:   int64(binary.BigEndian.Uint64(rec[12:20])),
			PTS:          binary.BigEndian.Uint64(rec[20:28]),
		}
	}
	return ix, nil
}

// ErrIndexMiss is returned when a seek target precedes every keyframe.
var ErrIndexMiss = errors.New("no keyframe at or before target")
