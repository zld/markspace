package scan

import (
	"encoding/binary"
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"mediad/internal/model"
)

// maxEmsgPayload bounds the buffer used to parse an emsg box. Event messages
// are small metadata; a box larger than this is treated as unparseable rather
// than allowed to grow memory without limit.
const maxEmsgPayload = 1 << 20 // 1 MiB

// readEmsg decodes an emsg box. go-mp4 v1.6.0 decodes the version-1 field
// order incorrectly (it reads PresentationTime before Timescale contrary to
// ISO/IEC 23009-1), so both versions are parsed here from the raw payload,
// which keeps one correct, spec-faithful implementation.
func (st *mp4State) readEmsg(h *mp4.ReadHandle) {
	var buf limitedBuffer
	if _, err := h.ReadData(&buf); err != nil {
		st.emsgUnparseable(h, err)
		return
	}
	if len(buf.b) > maxEmsgPayload {
		st.emsgUnparseable(h, fmt.Errorf("emsg payload too large: %d bytes", len(buf.b)))
		return
	}
	// ReadData yields the full-box payload; its first byte is the version
	// (followed by three flag bytes), then SchemeIdUri begins.
	ev, ok := parseEmsgBox(buf.b)
	if !ok {
		st.emsgUnparseable(h, fmt.Errorf("malformed emsg"))
		return
	}
	ev.Offset = int64(h.BoxInfo.Offset)
	ev.Length = int64(h.BoxInfo.Size)
	st.res.Events = append(st.res.Events, ev)
}

// limitedBuffer is an io.Writer that retains everything written; the size
// guard in readEmsg bounds its growth.
type limitedBuffer struct{ b []byte }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	l.b = append(l.b, p...)
	return len(p), nil
}

// parseEmsgBox decodes a full emsg payload including the 4-byte full-box
// version/flags header. Returns ok=false on a truncated/malformed box.
func parseEmsgBox(raw []byte) (TimedEvent, bool) {
	if len(raw) < 4 {
		return TimedEvent{Kind: EventEmsg}, false
	}
	version := raw[0]
	return parseEmsgPayload(raw[4:], version)
}

// parseEmsgPayload decodes an emsg payload (after the full-box header) for
// either wire version. Returns ok=false on a truncated/malformed box.
func parseEmsgPayload(raw []byte, version byte) (TimedEvent, bool) {
	ev := TimedEvent{Kind: EventEmsg}
	p := newByteReader(raw)

	scheme, ok := p.cString()
	if !ok {
		return ev, false
	}
	value, ok := p.cString()
	if !ok {
		return ev, false
	}
	ev.Scheme = scheme
	_ = value

	timescale, ok := p.u32()
	if !ok || timescale == 0 {
		return ev, false
	}
	ev.TimeBase = model.Rational{Num: 1, Den: int64(timescale)}

	if version == 1 {
		// v1: timescale, presentation_time(u64), event_duration(u32), id(u32)
		pt, ok := p.u64()
		if !ok {
			return ev, false
		}
		ev.Tick = int64(pt)
	} else {
		// v0: timescale, presentation_time_delta(u32), event_duration(u32), id(u32)
		delta, ok := p.u32()
		if !ok {
			return ev, false
		}
		ev.Tick = int64(delta)
	}
	dur, ok := p.u32()
	if !ok {
		return ev, false
	}
	ev.DurTick = int64(dur)
	id, ok := p.u32()
	if !ok {
		return ev, false
	}
	ev.ID = fmt.Sprintf("emsg-%d", id)
	ev.Payload = append([]byte(nil), p.rest()...)
	return ev, true
}

func (st *mp4State) emsgUnparseable(h *mp4.ReadHandle, err error) {
	st.res.Anomalies = append(st.res.Anomalies, model.Anomaly{
		Type:         model.AnomalyUnparseable,
		Message:      "unparseable emsg: " + err.Error(),
		SourceOffset: int64(h.BoxInfo.Offset),
		SourceLength: int64(h.BoxInfo.Size),
	})
}

// byteReader is a tiny bounded cursor over an emsg payload.
type byteReader struct {
	b []byte
	i int
}

func newByteReader(b []byte) *byteReader { return &byteReader{b: b} }

func (r *byteReader) cString() (string, bool) {
	start := r.i
	for r.i < len(r.b) {
		if r.b[r.i] == 0 {
			s := string(r.b[start:r.i])
			r.i++
			return s, true
		}
		r.i++
	}
	return "", false
}

func (r *byteReader) u32() (uint32, bool) {
	if r.i+4 > len(r.b) {
		return 0, false
	}
	v := binary.BigEndian.Uint32(r.b[r.i:])
	r.i += 4
	return v, true
}

func (r *byteReader) u64() (uint64, bool) {
	if r.i+8 > len(r.b) {
		return 0, false
	}
	v := binary.BigEndian.Uint64(r.b[r.i:])
	r.i += 8
	return v, true
}

func (r *byteReader) rest() []byte { return r.b[r.i:] }
