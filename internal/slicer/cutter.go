package slicer

import (
	"errors"
	"io"

	"hlsflow/internal/ts"
)

// Cut describes one output segment produced by cutting a source stream.
type Cut struct {
	// StartPTS and EndPTS are unwrapped 90kHz timestamps.
	StartPTS int64
	EndPTS int64
	// Data is the reassigned TS payload (PSI prefix plus source packets).
	Data []byte
}

// Cutter splits a single source transport stream at IDR boundaries while
// preserving PSI continuity at the head of each output.
type Cutter struct {
	parser *ts.Parser
	patPMT [][]byte // raw PSI packets to prepend to every output
	pending  [][]byte
	open     bool
	start    int64
	timeline ts.Timeline
}

// NewCutter requires a parser; the video PID is resolved lazily because the
// PMT that declares it only arrives during the first Read calls.
func NewCutter(p *ts.Parser) *Cutter {
	return &Cutter{parser: p}
}

// cachePSI stores raw PAT/PMT packets so each emitted segment starts with
// full program information.
func (c *Cutter) cachePSI(pkt ts.Packet, raw []byte) {
	if pkt.PID == ts.PIDPAT || pkt.PID == c.parser.PMTPID {
		c.patPMT = append(c.patPMT, append([]byte(nil), raw...))
	}
}

// Run consumes the parser to EOF and returns cuts plus the first/last PTS.
// targetTicks selects the ideal cut spacing; the nearest IDR boundary wins.
// A truncated tail or CC jump is reported after complete cuts have flushed.
func (c *Cutter) Run(targetTicks int64) (cuts []Cut, firstPTS, lastPTS int64, err error) {
	haveFirst := false
	nextCutAt := int64(-1)
	flush := func(endPTS int64) {
		if !c.open || len(c.pending) == 0 {
			return
		}
		data := make([]byte, 0, len(c.patPMT)*ts.PacketSize+len(c.pending)*ts.PacketSize)
		for _, psi := range c.patPMT {
			data = append(data, psi...)
		}
		for _, p := range c.pending {
			data = append(data, p...)
		}
		cuts = append(cuts, Cut{StartPTS: c.start, EndPTS: endPTS, Data: data})
		c.pending = nil
		c.open = false
	}
	for {
		pkt, readErr := c.parser.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if errors.Is(readErr, ts.ErrTruncated) {
			flush(lastPTS)
			return cuts, firstPTS, lastPTS, ts.ErrTruncated
		}
		if readErr != nil {
			flush(lastPTS)
			return cuts, firstPTS, lastPTS, readErr
		}
		raw := c.parser.RawPacket(pkt)
		c.cachePSI(pkt, raw)
		isIDR, pts := c.videoBoundary(pkt)
		if isIDR {
			if !haveFirst {
				haveFirst = true
				firstPTS = pts
			}
			// Cut when this IDR is at/after the scheduled target, choosing
			// the nearest boundary the stream actually offers.
			if c.open && pts >= nextCutAt {
				flush(pts)
			}
			if !c.open {
				c.open = true
				c.start = pts
				nextCutAt = pts + targetTicks
			}
		}
		if c.open {
			c.pending = append(c.pending, raw)
			if pts > lastPTS {
				lastPTS = pts
			}
		}
	}
	flush(lastPTS)
	return cuts, firstPTS, lastPTS, nil
}

// videoBoundary reports whether pkt begins an IDR access unit and its PTS.
func (c *Cutter) videoBoundary(pkt ts.Packet) (bool, int64) {
	videoPID := c.parser.VideoPID()
	if videoPID == 0 || pkt.PID != videoPID || !pkt.PUSI || !pkt.HasPayload {
		return false, 0
	}
	info, err := ts.ParsePES(pkt.Payload)
	if err != nil || !info.HasPTS {
		return false, 0
	}
	pts, _ := c.timeline.ObservePTS(info.PTS)
	return ts.ContainsIDR(info.Data), pts
}

// DurationSeconds converts a cut's PTS span to seconds.
func (cu Cut) DurationSeconds() float64 {
	return ts.Seconds(cu.EndPTS - cu.StartPTS)
}
