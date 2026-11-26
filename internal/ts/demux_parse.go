package ts

import (
	"rtpregress/internal/clock"
)

// tsHeader is the decoded 4-byte MPEG-TS header.
type tsHeader struct {
	PID           uint16
	PUSI          bool
	HasPayload    bool
	HasAdaptation bool
	CC            uint8
}

func parseHeader(pkt []byte) tsHeader {
	return tsHeader{
		PID:           uint16(pkt[1]&0x1F)<<8 | uint16(pkt[2]),
		PUSI:          pkt[1]&0x40 != 0,
		HasPayload:    pkt[3]&0x10 != 0,
		HasAdaptation: pkt[3]&0x20 != 0,
		CC:            pkt[3] & 0x0F,
	}
}

// payloadBytes returns the TS packet payload after header/adaptation field.
func payloadBytes(pkt []byte, h tsHeader, off int64) ([]byte, error) {
	pos := hdrSize
	if h.HasAdaptation {
		if len(pkt) <= afLenIndex {
			return nil, clock.New(clock.CodeTSSync, "ts", off, 0, 0,
				"adaptation field length missing")
		}
		pos = afLenIndex + 1 + int(pkt[afLenIndex])
	}
	if pos >= len(pkt) {
		if h.HasPayload {
			return nil, clock.New(clock.CodeTSTruncated, "ts", off, 0, 0,
				"payload claims bytes past packet end at %d", pos)
		}
		return nil, nil
	}
	return pkt[pos:], nil
}

// checkContinuity enforces per-PID increment semantics. Duplicate CC with the
// same payload presence is legal; every other jump is a CC discontinuity. On a
// jump the observed CC is adopted as the new baseline (resync), so the next
// packet is judged against it rather than re-triggering the same fault.
func (d *Demuxer) checkContinuity(h tsHeader, st *pidState, off int64) error {
	if !h.HasPayload || h.PID == 0x1FFF {
		return nil
	}
	if st.hasCC {
		want := (st.lastCC + 1) & 0x0F
		if h.CC != want && h.CC != st.lastCC {
			d.stats.CCJumps++
			err := clock.New(clock.CodeTSCCJump,
				"ts:pid=0x"+hexPID(h.PID), off, st.lastPTSNS, 0,
				"CC %d -> %d, want %d", st.lastCC, h.CC, want)
			st.lastCC = h.CC // resync to the observed value
			return err
		}
	}
	st.hasCC = true
	st.lastCC = h.CC
	return nil
}

// AnchorRTP records the RTP 90 kHz timestamp of the bytes about to be fed.
// The pipeline calls it once per RTP record so PCR observations can be paired
// with the RTP media clock for drift estimation.
func (d *Demuxer) AnchorRTP(rtpTimestamp uint32, offset int64) {
	ticks, err := d.rtpUnwrap.Unwrap(rtpTimestamp, offset)
	if err == nil {
		d.lastRTPNanos = clock.RTPTicksToNanos(ticks)
		d.haveRTP = true
	}
}

func (d *Demuxer) processAdaptation(pkt []byte, h tsHeader, off int64) error {
	if len(pkt) <= afLenIndex+1 {
		return clock.New(clock.CodeTSTruncated, "ts", off, 0, 0,
			"adaptation field truncated")
	}
	afLen := int(pkt[afLenIndex])
	if afLen == 0 {
		return nil // one-byte stuffing only
	}
	start := afLenIndex + 1
	if start+afLen > len(pkt) {
		return clock.New(clock.CodeTSTruncated, "ts", off, 0, 0,
			"adaptation field length %d overruns packet", afLen)
	}
	flags := pkt[start]
	if flags&0x10 == 0 || afLen < 7 {
		return nil // no PCR field
	}
	b := pkt[start+1 : start+7]
	base := int64(b[0])<<25 | int64(b[1])<<17 | int64(b[2])<<9 |
		int64(b[3])<<1 | int64(b[4])>>7
	ext := int64(b[4]&0x01)<<8 | int64(b[5])
	raw := base*300 + ext
	unwrapped, err := d.pcrUnwrap.Unwrap(raw, flags&0x80 != 0, off)
	if err != nil {
		return err
	}
	d.stats.PCRSeen++
	pcrNanos := d.pcrToGlobal(unwrapped)
	if d.haveRTP {
		d.drift.Add(d.lastRTPNanos, pcrNanos)
	}
	return nil
}

// pcrToGlobal places unwrapped PCR ticks on a global line whose first PCR is 0.
func (d *Demuxer) pcrToGlobal(ticks int64) int64 {
	ns := clock.PCRToNanos(ticks)
	if !d.baseSet {
		d.baseNS = -ns
		d.baseSet = true
	}
	return d.baseNS + ns
}

// processAudioPES assembles PES packets on audio PIDs and stamps their PTS.
func (d *Demuxer) processAudioPES(h tsHeader, st *pidState, payload []byte, off int64,
	onEvent func(TimelineEvent)) {
	if h.PUSI {
		d.flushPES(h.PID, st, onEvent)
		st.pesBuf = append(st.pesBuf[:0], trimPESPayload(payload)...)
		st.pesStart = off
		rawPTS, rawDTS, hasDTS, err := parsePESHeader(st.pesBuf)
		if err != nil {
			st.pesHasTS = false
			_ = d.fail(err)
			return
		}
		pts := d.ptsUnwrap.Unwrap(rawPTS)
		dts := pts
		if hasDTS {
			// DTS shares the same 33-bit line; unwrap against PTS phase
			dts = d.ptsUnwrap.Unwrap(rawDTS)
		}
		// The global line is the unwrapped 90 kHz clock itself; the first
		// PES keeps its own presentation time rather than being forced to 0.
		st.pesHasTS = true
		st.pesPTS = clock.RTPTicksToNanos(pts)
		if hasDTS {
			st.pesDTS = clock.RTPTicksToNanos(dts)
		} else {
			st.pesDTS = st.pesPTS
		}
		return
	}
	if len(st.pesBuf) == 0 {
		// continuation without start: isolated packet, skip
		return
	}
	st.pesBuf = append(st.pesBuf, payload...)
}

func (d *Demuxer) flushPES(pid uint16, st *pidState, onEvent func(TimelineEvent)) {
	if len(st.pesBuf) < pesHeaderSize || !st.pesHasTS {
		st.pesBuf = st.pesBuf[:0]
		return
	}
	es := extractES(st.pesBuf)
	if len(es) == 0 {
		st.pesBuf = st.pesBuf[:0]
		return
	}
	pes := &PESPacket{
		PID:        pid,
		Offset:     st.pesStart,
		PTSNanos:   st.pesPTS,
		DTSNanos:   st.pesDTS,
		Data:       es,
		StreamType: st.streamType,
	}
	// monotonic guard per PID: never publish a PES placed before the last one
	if st.lastPTSNS != 0 && pes.PTSNanos < st.lastPTSNS-clock.MaxSaneBackwardNanos {
		_ = d.fail(clock.New(clock.CodeTimeBackwards, "ts:pid=0x"+hexPID(pid),
			st.pesStart, st.lastPTSNS, pes.PTSNanos, "PES PTS went backwards"))
		st.pesBuf = st.pesBuf[:0]
		return
	}
	if pes.PTSNanos > st.lastPTSNS || st.lastPTSNS == 0 {
		st.lastPTSNS = pes.PTSNanos
	}
	d.stats.PESPackets++
	onEvent(TimelineEvent{PES: pes})
	st.pesBuf = st.pesBuf[:0]
}

// flushAll emits any in-flight PES at clean end of stream.
func (d *Demuxer) flushAll(onEvent func(TimelineEvent)) {
	for pid, st := range d.pids {
		if len(st.pesBuf) > 0 {
			d.flushPES(pid, st, onEvent)
		}
	}
}

// Stats exposes the running counters (tests use it mid-stream).
func (d *Demuxer) Stats() Stats { return d.stats }

func hexPID(pid uint16) string {
	const hex = "0123456789abcdef"
	return string([]byte{hex[pid>>12&0xF], hex[pid>>8&0xF], hex[pid>>4&0xF], hex[pid&0xF]})
}
