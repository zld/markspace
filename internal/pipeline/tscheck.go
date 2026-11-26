package pipeline

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
	"liveregress/internal/ts"
)

// tsCheck summarises a transport-stream probe for the report.
type tsCheck struct {
	Path       string
	Packets    int64
	Tracks     int
	Samples    int
	Keyframes  int
	RTPPackets int
	RTPSeqGaps int
	RTPStart   string
	Truncated  bool
}

// probeTS demuxes a transport-stream segment, verifies every retained access
// unit is byte-identical to the file at its recorded offset, and (when an RTP
// sidecar is present) checks sequence and timestamp continuity against the
// same media clock.
func (r *Runner) probeTS(segPath string) (*tsCheck, error) {
	abs := filepath.Join(r.opts.Root, filepath.FromSlash(segPath))
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fault.Wrap(fault.KindPath, segPath, -1, "read transport stream", err)
	}
	if int64(len(raw)) > r.opts.MaxSegmentBytes {
		return nil, fault.New(fault.KindLimit, segPath, 0,
			"transport stream exceeds the per-segment limit").Recover(true)
	}
	d, derr := ts.DemuxFile(bytes.NewReader(raw), segPath)
	check := &tsCheck{Path: segPath}
	if d != nil {
		check.Packets = d.Packets
		check.Tracks = len(d.Tracks)
		for _, tr := range d.Tracks {
			check.Samples += len(tr.Samples)
			for _, s := range tr.Samples {
				if s.Keyframe {
					check.Keyframes++
				}
				var got []byte
				for _, span := range s.PayloadRanges {
					if span.Offset < 0 || span.Offset+span.Length > int64(len(raw)) {
						return nil, fault.New(fault.KindTruncate, segPath, span.Offset,
							"access unit payload extends past end of file").Recover(false)
					}
					got = append(got, raw[span.Offset:span.Offset+span.Length]...)
				}
				if !bytes.Equal(got, s.AUBytes) {
					return nil, fault.New(fault.KindVerify, segPath, s.AUOffset,
						"retained access unit bytes do not match the input").Recover(false)
				}
			}
		}
	}
	if derr != nil {
		fe, ok := fault.As(derr)
		if !ok || !fe.Recoverable {
			return nil, derr
		}
		check.Truncated = true
	}
	rtpRel := segPath + ".rtp"
	rtpAbs := filepath.Join(r.opts.Root, filepath.FromSlash(rtpRel))
	if rtpData, rerr := os.ReadFile(rtpAbs); rerr == nil {
		info, ierr := r.rtpContinuity(rtpData, 90000)
		if ierr != nil {
			return nil, ierr
		}
		check.RTPPackets = info.Count
		check.RTPSeqGaps = info.SequenceGaps
		if start, ok := info.Start(); ok {
			check.RTPStart = start.String()
		}
		if info.SequenceGaps > 0 {
			return check, fault.New(fault.KindGap, rtpRel, 0,
				fmt.Sprintf("RTP sequence gaps=%d", info.SequenceGaps)).Recover(true)
		}
	}
	return check, nil
}

// rtpContinuity parses a length-prefixed RTP packet sidecar and tracks
// sequence/timestamp continuity at clockRate.
func (r *Runner) rtpContinuity(data []byte, clockRate uint32) (*clock.RTPClock, error) {
	c := clock.NewRTPClock(clockRate)
	pos := 0
	for pos+2 <= len(data) {
		n := int(data[pos])<<8 | int(data[pos+1])
		pos += 2
		if n < 12 || pos+n > len(data) {
			return c, fault.New(fault.KindCorrupt, "rtp", int64(pos),
				fmt.Sprintf("invalid RTP packet length %d", n)).Recover(false)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(data[pos : pos+n]); err != nil {
			return c, fault.Wrap(fault.KindCorrupt, "rtp", int64(pos), "unmarshal RTP", err).Recover(false)
		}
		if err := c.Add(&pkt); err != nil {
			return c, fault.Wrap(fault.KindGap, "rtp", int64(pos), "RTP continuity", err).Recover(true)
		}
		pos += n
	}
	return c, nil
}
