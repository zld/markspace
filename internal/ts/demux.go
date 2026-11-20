package ts

import (
	"fmt"
	"io"
)

// Demuxer turns a raw transport stream into an ordered stream of video
// frames. It discovers PAT/PMT on the fly, reassembles PES packets split
// across TS packets and keeps per-PID timestamp and continuity state.
//
// All intermediate buffers hold at most one frame's worth of packets, so
// memory consumption does not grow with total stream duration.
type Demuxer struct {
	pr     *PacketReader
	cc     *ContinuityTracker
	patAsm *SectionAssembler
	pmtAsm *SectionAssembler
	ptsUnw *PTSUnwrapper
	dtsUnw *PTSUnwrapper

	pmtPID   int
	videoPID int

	// PES reassembly state for the video PID.
	pesBuf      []byte
	pesStartOff int64
	ccErrors    []CCError
	ccTotal     int

	// LastFrame / LastErr implement the common scanner-style API.
	LastFrame *Frame
	LastErr   error
}

// maxRetainedCCErrors bounds the per-stream error sample list; the full
// count stays available via CCTotal so long streams cannot grow memory.
const maxRetainedCCErrors = 32

// NewDemuxer wraps r. PSI discovery happens lazily during the first Scan.
func NewDemuxer(r io.Reader) *Demuxer {
	return &Demuxer{
		pr:     NewPacketReader(r),
		cc:     NewContinuityTracker(),
		patAsm: NewSectionAssembler(PIDPAT),
		ptsUnw: &PTSUnwrapper{},
		dtsUnw: &PTSUnwrapper{},
		pmtPID: -1, videoPID: -1,
	}
}

// VideoPID returns the PID discovered from the PMT, or -1 before discovery.
func (d *Demuxer) VideoPID() int { return d.videoPID }

// CCErrors returns a bounded sample of continuity problems (the most recent
// maxRetainedCCErrors entries). Use CCTotal for the full occurrence count.
func (d *Demuxer) CCErrors() []CCError { return d.ccErrors }

// CCTotal reports the total number of continuity errors observed, including
// entries beyond the retained sample window.
func (d *Demuxer) CCTotal() int { return d.ccTotal }

// Scan advances to the next complete video frame. It returns false at clean
// end of stream or after a parse error, inspectable via LastErr.
func (d *Demuxer) Scan() bool {
	for {
		p, err := d.pr.ReadPacket()
		if err != nil {
			d.LastErr = err
			return false
		}
		if ev, expected := d.cc.Observe(p); ev == CCDuplicate || ev == CCJump {
			d.ccTotal++
			// Keep only the most recent bounded window of concrete samples.
			if len(d.ccErrors) >= maxRetainedCCErrors {
				copy(d.ccErrors, d.ccErrors[1:])
				d.ccErrors = d.ccErrors[:len(d.ccErrors)-1]
			}
			d.ccErrors = append(d.ccErrors, CCError{
				PID: p.PID, Offset: p.Offset, Got: p.ContinuityCounter,
				Expected: expected, Kind: ev,
			})
		}

		switch {
		case p.PID == PIDPAT:
			if section, perr := d.patAsm.Feed(p); perr == nil && section != nil {
				if _, pmtPID, perr := ParsePAT(section); perr == nil && pmtPID != 0 {
					if d.pmtPID != pmtPID {
						d.pmtPID = pmtPID
						d.pmtAsm = NewSectionAssembler(pmtPID)
					}
				}
			}
		case d.pmtPID >= 0 && p.PID == d.pmtPID:
			if section, perr := d.pmtAsm.Feed(p); perr == nil && section != nil {
				if pm, perr := ParsePMT(section); perr == nil && pm != nil {
					d.videoPID = pm.VideoPID
				}
			}
		case d.videoPID >= 0 && p.PID == d.videoPID:
			if frame, ok := d.feedVideo(p); ok {
				d.LastFrame = frame
				d.LastErr = nil
				return true
			}
		}
	}
}

// feedVideo handles one video-PID packet and emits a Frame when a PES packet
// completes at the next payload-unit-start indicator.
func (d *Demuxer) feedVideo(p *Packet) (*Frame, bool) {
	if p.PUSI {
		var completed *Frame
		if len(d.pesBuf) > 0 {
			completed = d.buildFrame()
		}
		d.pesBuf = append(d.pesBuf[:0], p.Payload...)
		d.pesStartOff = p.Offset
		return completed, completed != nil
	}
	if len(d.pesBuf) > 0 && len(p.Payload) > 0 {
		d.pesBuf = append(d.pesBuf, p.Payload...)
	}
	return nil, false
}

// buildFrame parses the accumulated PES packet into a Frame.
func (d *Demuxer) buildFrame() *Frame {
	es, pts33, dts33, err := ParsePESHeader(d.pesBuf)
	if err != nil {
		d.LastErr = fmt.Errorf("PES at offset %d: %w", d.pesStartOff, err)
		return nil
	}
	f := &Frame{
		Keyframe:    IsIDRAccessUnit(es),
		StartOffset: d.pesStartOff,
	}
	if pts33 != 0 {
		f.PTS = d.ptsUnw.Push(pts33)
		f.DTS = d.dtsUnw.Push(dts33)
	}
	return f
}

// Flush emits the final buffered frame at end of stream; nil if none.
func (d *Demuxer) Flush() *Frame {
	if len(d.pesBuf) == 0 {
		return nil
	}
	return d.buildFrame()
}

// ResetCCErrors clears the accumulated continuity error list and count.
func (d *Demuxer) ResetCCErrors() {
	d.ccErrors = d.ccErrors[:0]
	d.ccTotal = 0
}
