package adapter

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"mediad/timeline"
)

// Self-describing metadata container produced by the local fixture generator:
//
//	"MDM1" magic, uint32 record count, then per record:
//	uint32 length, uint32 track_id, int64 tick, uint8 kind, uint8 flags,
//	uint16 meta_type length + meta_type, payload (length covers fields after it).
const metadataMagic = "MDM1"

// Metadata record kind codes.
const (
	kindSubtitle byte = 1
	kindChapter  byte = 2
	kindID3      byte = 3
	kindEmsg     byte = 4
	kindSEI      byte = 5
	// kind values >= 200 are unknown and must be preserved raw.
	kindUnknownFloor byte = 200
)

// MetadataAdapter parses subtitle/chapter/ID3/emsg/SEI metadata streams.
type MetadataAdapter struct{}

func (MetadataAdapter) Name() string     { return "metadata" }
func (MetadataAdapter) Version() Version { return Version{Major: 1, Minor: 0} }

func (MetadataAdapter) Parse(r io.Reader, opts Options) (*ParseResult, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(data) < 8 {
		return nil, fmt.Errorf("metadata: truncated header: %d bytes", len(data))
	}
	if string(data[:4]) != metadataMagic {
		return nil, fmt.Errorf("metadata: bad magic %q", data[:4])
	}
	count := binary.BigEndian.Uint32(data[4:8])
	pos := 8
	byTrack := map[uint32]*timeline.Track{}
	var order []uint32
	res := &ParseResult{}
	var seq int64
	for i := uint32(0); i < count; i++ {
		if pos+4 > len(data) {
			return nil, fmt.Errorf("metadata: truncated record %d length field", i)
		}
		recLen := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		pos += 4
		if recLen < 20 {
			return nil, fmt.Errorf("metadata: illegal length record %d: len=%d", i, recLen)
		}
		if pos+recLen > len(data) {
			return nil, fmt.Errorf("metadata: truncated record %d: declares %d bytes, %d remain", i, recLen, len(data)-pos)
		}
		rec := data[pos : pos+recLen]
		pos += recLen
		trackID := binary.BigEndian.Uint32(rec[0:4])
		tick := int64(binary.BigEndian.Uint64(rec[4:12]))
		kindCode := rec[12]
		flags := rec[13]
		mtLen := int(binary.BigEndian.Uint16(rec[14:16]))
		if 16+mtLen > recLen {
			return nil, fmt.Errorf("metadata: illegal meta_type length record %d", i)
		}
		metaType := string(rec[16 : 16+mtLen])
		payload := rec[16+mtLen:]
		if trackID == 0 {
			trackID = opts.TrackID
		}
		tr, ok := byTrack[trackID]
		if !ok {
			tr = &timeline.Track{ID: trackID, Clock: clockFrom(opts, trackID)}
			byTrack[trackID] = tr
			order = append(order, trackID)
		}
		ev := timeline.Event{TrackID: trackID, Tick: tick, Seq: seq, Payload: append([]byte(nil), payload...), MetaType: metaType}
		ev.Keyframe = flags&1 != 0
		ev.Corrupt = flags&2 != 0
		switch {
		case kindCode >= kindUnknownFloor:
			ev.Kind = timeline.KindUnknown
			res.Unknown = append(res.Unknown, timeline.UnknownMeta{MetaType: metaType, Raw: append([]byte(nil), payload...)})
		default:
			ev.Kind = decodeKind(kindCode)
			if ev.Kind == timeline.KindUnknown {
				return nil, fmt.Errorf("metadata: unknown record kind %d at record %d", kindCode, i)
			}
			ev.Text = string(payload)
		}
		tr.Events = append(tr.Events, ev)
		seq++
	}
	if pos != len(data) {
		return nil, fmt.Errorf("metadata: %d trailing bytes", len(data)-pos)
	}
	for _, id := range order {
		res.Tracks = append(res.Tracks, *byTrack[id])
	}
	return res, nil
}

func decodeKind(c byte) timeline.Kind {
	switch c {
	case kindSubtitle:
		return timeline.KindSubtitle
	case kindChapter:
		return timeline.KindChapter
	case kindID3:
		return timeline.KindID3
	case kindEmsg:
		return timeline.KindEmsg
	case kindSEI:
		return timeline.KindSEI
	default:
		return timeline.KindUnknown
	}
}

func clockFrom(opts Options, trackID uint32) timeline.Clock {
	ts := opts.Timescale
	if ts <= 0 {
		ts = 1000
	}
	return timeline.Clock{Timescale: ts, OriginNS: opts.OriginNS, WrapModulus: opts.WrapModulus, MaxDriftNS: opts.MaxDriftNS}
}

// --- fixture writer used by tests and the demo generator ---

// MetadataWriter emits the self-describing metadata container.
type MetadataWriter struct {
	buf bytes.Buffer
	n   uint32
}

func NewMetadataWriter() *MetadataWriter {
	w := &MetadataWriter{}
	w.buf.WriteString(metadataMagic)
	w.buf.Write([]byte{0, 0, 0, 0}) // count placeholder
	return w
}

// WriteRecord appends one record. kindCode >= 200 means unknown metadata.
func (w *MetadataWriter) WriteRecord(trackID uint32, tick int64, kindCode byte, flags byte, metaType string, payload []byte) {
	var body bytes.Buffer
	_ = binary.Write(&body, binary.BigEndian, trackID)
	_ = binary.Write(&body, binary.BigEndian, uint64(tick))
	body.WriteByte(kindCode)
	body.WriteByte(flags)
	_ = binary.Write(&body, binary.BigEndian, uint16(len(metaType)))
	body.WriteString(metaType)
	body.Write(payload)
	_ = binary.Write(&w.buf, binary.BigEndian, uint32(body.Len()))
	w.buf.Write(body.Bytes())
	w.n++
}

// Bytes finalizes the count field and returns the complete container.
func (w *MetadataWriter) Bytes() []byte {
	out := w.buf.Bytes()
	binary.BigEndian.PutUint32(out[4:8], w.n)
	return append([]byte(nil), out...)
}
