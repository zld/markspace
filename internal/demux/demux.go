// Package demux turns a Matroska/WebM byte stream into an ordered stream of
// codec-agnostic packets. It owns the transition from raw EBML structures to
// the shared domain types: track registry, nanosecond timestamps and
// per-block input byte ranges with untouched payloads.
package demux

import (
	"io"

	"mediapack/internal/clock"
	"mediapack/internal/ebml"
	"mediapack/internal/fault"
	"mediapack/internal/track"
)

// Packet is one demuxed media block. Payload is the raw codec data copied out
// of the input; the pipeline never transcodes or re-encodes it.
type Packet struct {
	// Sequence is the zero-based index of this packet in global stream order.
	Sequence int
	// InputTrack is the Matroska track number.
	InputTrack uint32
	// OutputTrack is the stable 1-based index from the track registry.
	OutputTrack uint32
	// TimestampNS is the cluster+block timecode converted to nanoseconds.
	TimestampNS int64
	// Keyframe mirrors the SimpleBlock keyframe flag.
	Keyframe bool
	// InputStart/InputEnd bound the codec payload inside the source file.
	InputStart int64
	InputEnd   int64
	// Payload is a private copy of [InputStart, InputEnd).
	Payload []byte
}

// Demuxer reads one seekable WebM input end to end.
type Demuxer struct {
	rs       io.ReadSeeker
	seg      *ebml.Segment
	registry *track.Registry
	monitors map[uint32]*clock.Monitor
	scaleNS  uint64
	mapping  *track.Mapping
	sequence int
}

// Open parses metadata, enforces the codec whitelist and builds the stable
// track mapping. The input offset of any rejected track is preserved in the
// returned structured fault.
func Open(rs io.ReadSeeker) (*Demuxer, error) {
	seg, err := ebml.OpenSegment(rs)
	if err != nil {
		return nil, err
	}
	scaleNS, rawTracks, err := seg.ReadMetadata()
	if err != nil {
		return nil, err
	}
	registry := track.NewRegistry()
	nanosPerSecond := uint64(clock.NanosecondsPerSecond)
	for _, rt := range rawTracks {
		// Matroska timestamps are ticks at the segment timestamp scale; the
		// 1e9 tick-rate timebase makes one nanosecond the unit after the
		// scale multiplication performed by the demuxer.
		if _, rerr := registry.Register(uint32(rt.Number), rt.CodecID, nanosPerSecond); rerr != nil {
			if fe := fault.Of(rerr); fe != nil && fe.Offset < 0 {
				fe.Offset = rt.HeaderStart
			}
			return nil, rerr
		}
	}
	monitors := make(map[uint32]*clock.Monitor, len(rawTracks))
	for _, rt := range rawTracks {
		tb, _ := clock.New(nanosPerSecond, 0)
		monitors[uint32(rt.Number)] = clock.NewMonitor(int(rt.Number), tb)
	}
	return &Demuxer{
		rs:       rs,
		seg:      seg,
		registry: registry,
		monitors: monitors,
		scaleNS:  scaleNS,
		mapping:  registry.Mapping(),
	}, nil
}

// Mapping publishes the deterministic input->output track mapping.
func (d *Demuxer) Mapping() *track.Mapping { return d.mapping }

// TimestampScaleNS reports the segment timestamp scale in nanoseconds/tick.
func (d *Demuxer) TimestampScaleNS() uint64 { return d.scaleNS }

// Stream walks every block in file order, invoking onPacket for each one. The
// first structured fault (truncation, timestamp regression, ...) stops the
// walk and is returned; packets already emitted remain valid.
func (d *Demuxer) Stream(onPacket func(*Packet) error) error {
	return d.seg.ScanClusters(func(c *ebml.Cluster) error {
		for _, blk := range c.Blocks {
			tr, ok := d.registry.Lookup(blk.Track)
			if !ok {
				return fault.NewTrack(fault.KindUnsupportedCodec, int(blk.Track),
					blk.Start, "block references unregistered track %d", blk.Track)
			}
			absTicks := blk.AbsTimecode(c.Timestamp)
			ns := absTicks * int64(d.scaleNS)
			mon := d.monitors[blk.Track]
			ns, err := mon.CheckNanos(ns, blk.Start)
			if err != nil {
				return err
			}
			// A block whose declared payload runs past physical EOF is a
			// truncated tail even when a deferred header check admitted its
			// header. Reject it before any partial bytes can be delivered.
			if phys := d.seg.Reader().PhysicalEnd(); phys >= 0 && blk.PayloadEnd > phys {
				return fault.New(fault.KindTruncated, phys,
					"block payload [%d,%d) overruns file end %d",
					blk.PayloadStart, blk.PayloadEnd, phys).AsRecoverable()
			}
			payload, err := d.seg.Reader().ReadRange(blk.PayloadStart, blk.PayloadEnd)
			if err != nil {
				return err
			}
			p := &Packet{
				Sequence:    d.sequence,
				InputTrack:  blk.Track,
				OutputTrack: tr.OutputIndex,
				TimestampNS: ns,
				Keyframe:    blk.Keyframe,
				InputStart:  blk.PayloadStart,
				InputEnd:    blk.PayloadEnd,
				Payload:     payload,
			}
			d.sequence++
			if err := onPacket(p); err != nil {
				return err
			}
		}
		return nil
	})
}
