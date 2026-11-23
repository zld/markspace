// Package rtp scans a captured RTP/RTCP packet stream.
//
// The capture format is a minimal local framing so that a datagram's declared
// length survives even when the captured body was cut short:
//
//	file    := magic "MTCAPv1\n" record*
//	record  := capturedLen uint32 | originalLen uint32 | body[capturedLen]
//
// A record whose captured length is below its original length is a truncated
// datagram. Packet byte offsets refer to the first byte of the body, so every
// located fault stays within one RTP header length of the packet it describes.
package rtp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"mediatool/internal/clock"
	"mediatool/internal/fault"
)

// Magic identifies a capture stream.
var Magic = []byte("MTCAPv1\n")

// recordHeader is capturedLen + originalLen.
const recordHeader = 8

// Kind distinguishes packet events from sender reports.
type Kind uint8

const (
	EventPacket Kind = iota + 1
	EventSenderReport
	// EventFault carries only fault records (for example losses confirmed
	// after the reorder window has passed).
	EventFault
)

// TrackInfo binds an SSRC to a track identity and RTP clock rate.
type TrackInfo struct {
	TrackID   uint32
	ClockRate uint32
}

// Config controls a scan.
type Config struct {
	// Tracks maps SSRC to track identity. Unknown SSRCs yield a located fault.
	Tracks map[uint32]TrackInfo
	// Window bounds how many sequence numbers are remembered for reorder and
	// duplicate detection. Zero means 256.
	Window int
	// IsKeyframe optionally classifies a packet as a keyframe start.
	IsKeyframe func(ev *Event) bool
}

func (c Config) window() int {
	if c.Window <= 0 {
		return 256
	}
	return c.Window
}

// Event is one located item produced by the scanner.
type Event struct {
	Kind Kind
	// Offset is the first byte of the packet body in the input stream.
	Offset int64
	// Length is the captured body length; OriginalLength is what the sender
	// declared, which is larger for a truncated datagram.
	Length         int
	OriginalLength int

	TrackID     uint32
	SSRC        uint32
	Seq         uint16
	ExtSeq      uint64
	Timestamp   uint32
	PayloadType uint8
	Marker      bool
	Keyframe    bool
	Payload     []byte

	SR *clock.SenderReport

	Faults []*fault.Fault
}

// Err returns the most severe fault attached to the event, or nil.
func (e *Event) Err() error {
	for _, f := range e.Faults {
		if f.Severity >= fault.SeverityDamaged {
			return f
		}
	}
	if len(e.Faults) > 0 {
		return e.Faults[0]
	}
	return nil
}

// Stats summarizes a finished scan.
type Stats struct {
	Packets    int `json:"packets"`
	SRs        int `json:"srs"`
	Lost       int `json:"lost"`
	Reordered  int `json:"reordered"`
	Duplicates int `json:"duplicates"`
	Truncated  int `json:"truncated"`
}

// Scanner streams events from a capture.
type Scanner struct {
	cfg Config
	src *countingReader

	trackers map[uint32]*seqTracker
	stats    Stats

	pending []*fault.Fault // faults waiting to be delivered
	flushed bool
	done    bool
	err     error
}

// countingReader tracks absolute byte offsets.
type countingReader struct {
	r   io.Reader
	pos int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.pos += int64(n)
	return n, err
}

// NewScanner validates the capture header and returns a streaming scanner.
func NewScanner(r io.Reader, cfg Config) (*Scanner, error) {
	cr := &countingReader{r: r}
	hdr := make([]byte, len(Magic))
	if _, err := io.ReadFull(cr, hdr); err != nil {
		return nil, fmt.Errorf("read capture header: %w", err)
	}
	if string(hdr) != string(Magic) {
		return nil, fmt.Errorf("not an RTP capture: bad magic %q", hdr)
	}
	return &Scanner{cfg: cfg, src: cr, trackers: make(map[uint32]*seqTracker)}, nil
}

// Stats returns the running totals.
func (s *Scanner) Stats() Stats { return s.stats }

// Next returns the next event. ok is false at end of stream.
func (s *Scanner) Next() (Event, bool, error) {
	if s.err != nil {
		return Event{}, false, s.err
	}
	if len(s.pending) > 0 {
		f := s.pending[0]
		s.pending = s.pending[1:]
		return Event{Kind: EventFault, Offset: f.Offset, Faults: []*fault.Fault{f}}, true, nil
	}
	if s.flushed {
		return Event{}, false, nil
	}
	ev, err := s.readRecord()
	if err == io.EOF {
		s.flushLosses()
		s.flushed = true
		s.stats.Lost += s.countPending()
		for _, tr := range s.trackers {
			for _, rec := range tr.confirmAll() {
				s.pending = append(s.pending, rec)
			}
		}
		if len(s.pending) > 0 {
			f := s.pending[0]
			s.pending = s.pending[1:]
			return Event{Kind: EventFault, Offset: f.Offset, Faults: []*fault.Fault{f}}, true, nil
		}
		return Event{}, false, nil
	}
	if err != nil {
		s.err = err
		return Event{}, false, err
	}
	return ev, true, nil
}

func (s *Scanner) countPending() int {
	n := 0
	for _, tr := range s.trackers {
		n += len(tr.pending)
	}
	return n
}

func (s *Scanner) flushLosses() {
	// All outstanding losses are confirmed at end of stream; Next drains them.
	for _, tr := range s.trackers {
		_ = tr
	}
}

func (s *Scanner) readRecord() (Event, error) {
	var hdr [recordHeader]byte
	if _, err := io.ReadFull(s.src, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return Event{}, fmt.Errorf("truncated capture record header at offset=%d: %w", s.src.pos, err)
		}
		return Event{}, err
	}
	captured := int(binary.BigEndian.Uint32(hdr[0:4]))
	original := int(binary.BigEndian.Uint32(hdr[4:8]))
	bodyStart := s.src.pos
	if captured < 0 || original < 0 || captured > 1<<24 || original > 1<<24 {
		return Event{}, fmt.Errorf("invalid capture record lengths at offset=%d: captured=%d original=%d", bodyStart, captured, original)
	}
	body := make([]byte, captured)
	if _, err := io.ReadFull(s.src, body); err != nil {
		return Event{}, fmt.Errorf("short capture body at offset=%d: %w", bodyStart, err)
	}
	if original < captured {
		original = captured
	}

	// A record whose captured length is below what the sender declared is a
	// truncated datagram. Its header may still be readable, so parse what
	// survived and attach the truncation fault to it.
	ev, err := s.parseBody(body, bodyStart, captured, original)
	if err != nil {
		return ev, err
	}
	if original > captured || captured < 12 {
		// Prefer the record-level explanation, and annotate the packet-level
		// truncation fault with the declared datagram length if one exists.
		replaced := false
		for _, f := range ev.Faults {
			if f.Kind == fault.KindTruncated {
				f.Length = original
				f.Detail = fmt.Sprintf("captured=%d original=%d (datagram cut short)", captured, original)
				replaced = true
			}
		}
		if !replaced {
			s.stats.Truncated++
			f := (&fault.Fault{
				Kind: fault.KindTruncated, Offset: bodyStart, Length: original,
				Recovery: fault.RecoverResync, Severity: fault.SeverityDamaged,
			}).WithDetail("captured=%d original=%d", captured, original)
			ev.Faults = append([]*fault.Fault{f}, ev.Faults...)
		}
		if ev.Length == 0 {
			ev.Length = captured
		}
		ev.OriginalLength = original
	}
	return ev, nil
}

func (s *Scanner) parseBody(body []byte, offset int64, captured, original int) (Event, error) {
	if body[0]>>6 != 2 {
		s.stats.Truncated++
		f := (&fault.Fault{Kind: fault.KindTruncated, Offset: offset, Length: captured,
			Recovery: fault.RecoverResync, Severity: fault.SeverityDamaged}).
			WithDetail("bad RTP/RTCP version %d", body[0]>>6)
		return Event{Kind: EventPacket, Offset: offset, Length: captured, OriginalLength: original,
			Faults: []*fault.Fault{f}}, nil
	}
	// RFC 5761 demux: RTCP packet types 192..223 in the second byte.
	if pt := body[1]; pt >= 192 && pt <= 223 {
		return s.parseRTCP(body, offset)
	}
	return s.parseRTP(body, offset, original)
}

func (s *Scanner) parseRTCP(body []byte, offset int64) (Event, error) {
	ev := Event{Kind: EventSenderReport, Offset: offset, Length: len(body), OriginalLength: len(body)}
	s.stats.SRs++
	sr, err := ParseSenderReport(body, offset)
	if err != nil {
		var f *fault.Fault
		if errors.As(err, &f) {
			ev.Faults = append(ev.Faults, f)
			if f.Kind == fault.KindTruncated {
				s.stats.Truncated++
			}
			return ev, nil
		}
		return ev, err
	}
	info, ok := s.cfg.Tracks[sr.SSRC]
	if !ok {
		ev.Faults = append(ev.Faults, (&fault.Fault{Kind: fault.KindLayout, Offset: offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityDamaged}).
			WithDetail("sender report for unknown ssrc=%d", sr.SSRC))
		return ev, nil
	}
	sr.TrackID = info.TrackID
	sr.ClockRate = info.ClockRate
	ev.TrackID = info.TrackID
	ev.SSRC = sr.SSRC
	ev.SR = sr
	return ev, nil
}

func (s *Scanner) parseRTP(body []byte, offset int64, original int) (Event, error) {
	ev := Event{Kind: EventPacket, Offset: offset, Length: len(body), OriginalLength: original}
	if len(body) < 12 {
		s.stats.Truncated++
		f := (&fault.Fault{Kind: fault.KindTruncated, Offset: offset, Length: len(body),
			Recovery: fault.RecoverResync, Severity: fault.SeverityDamaged}).
			WithDetail("RTP header needs 12 bytes, got %d", len(body))
		ev.Faults = append(ev.Faults, f)
		return ev, nil
	}
	ev.PayloadType = body[1] & 0x7f
	ev.Marker = body[1]&0x80 != 0
	ev.Seq = binary.BigEndian.Uint16(body[2:4])
	ev.Timestamp = binary.BigEndian.Uint32(body[4:8])
	ev.SSRC = binary.BigEndian.Uint32(body[8:12])

	payloadStart := 12
	// RFC 8285 one-byte and two-byte header extensions.
	if body[0]&0x10 != 0 {
		if len(body) < 16 {
			return s.truncatedRTP(ev, "header extension truncated")
		}
		extLen := int(binary.BigEndian.Uint16(body[14:16])) * 4
		payloadStart = 16 + extLen
		if payloadStart > len(body) {
			return s.truncatedRTP(ev, "header extension overruns packet")
		}
	}
	payloadEnd := len(body)
	if body[0]&0x20 != 0 { // padding
		pad := int(body[len(body)-1])
		if pad == 0 || payloadStart+pad > payloadEnd {
			return s.truncatedRTP(ev, "invalid RTP padding length")
		}
		payloadEnd -= pad
	}
	if payloadEnd < payloadStart {
		return s.truncatedRTP(ev, "negative payload length")
	}
	ev.Payload = body[payloadStart:payloadEnd]

	info, ok := s.cfg.Tracks[ev.SSRC]
	if !ok {
		ev.Faults = append(ev.Faults, (&fault.Fault{Kind: fault.KindLayout, Offset: offset,
			Recovery: fault.RecoverContinue, Severity: fault.SeverityDamaged}).
			WithDetail("packet for unknown ssrc=%d", ev.SSRC))
		return ev, nil
	}
	ev.TrackID = info.TrackID

	tr := s.trackers[info.TrackID]
	if tr == nil {
		tr = newSeqTracker(s.cfg.window())
		tr.trackID = info.TrackID
		s.trackers[info.TrackID] = tr
	}
	faults, ext := tr.observe(ev.Seq, offset, s.cfg.window())
	ev.ExtSeq = ext
	s.stats.Packets++
	for _, f := range faults {
		switch f.Kind {
		case fault.KindLoss:
			s.stats.Lost++
		case fault.KindReordered:
			s.stats.Reordered++
		case fault.KindDuplicate:
			s.stats.Duplicates++
		}
		ev.Faults = append(ev.Faults, f)
	}
	if s.cfg.IsKeyframe != nil {
		ev.Keyframe = s.cfg.IsKeyframe(&ev)
	}
	return ev, nil
}

func (s *Scanner) truncatedRTP(ev Event, detail string) (Event, error) {
	s.stats.Truncated++
	f := (&fault.Fault{Kind: fault.KindTruncated, Offset: ev.Offset, Length: ev.Length,
		Recovery: fault.RecoverResync, Severity: fault.SeverityDamaged}).WithDetail("%s", detail)
	ev.Faults = append(ev.Faults, f)
	ev.Payload = nil
	return ev, nil
}
