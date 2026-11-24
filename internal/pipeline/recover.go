package pipeline

import (
	"strconv"

	"github.com/pion/rtp"

	"archivecheck/internal/fixture"
	"archivecheck/internal/media"
	"archivecheck/internal/merr"
	"archivecheck/internal/transport"
)

// SegmentRecovery is the fully reassembled result for one segment.
type SegmentRecovery struct {
	Spec          media.TrackSpec
	Seq           uint32
	Frames        []media.Frame
	MediaBytes    int
	LostPackets   int
	DuplicatePkts int
	Truncated     bool
}

// recoverSegment runs the bounded reorder window over one capture,
// reassembles RTP payloads back into ARCH frames, reconciles them with
// the fMP4 sample table and marks every error condition explicitly.
func recoverSegment(spec media.TrackSpec, seq uint32, window int,
	capturePath, segmentPath string) (*SegmentRecovery, error) {

	pkts, err := readCapture(capturePath)
	if err != nil {
		return nil, err
	}
	buf, err := transport.NewReorderBuffer(window, spec.ID, seq)
	if err != nil {
		return nil, err
	}
	var ordered []*rtp.Packet
	for _, p := range pkts {
		out, err := buf.Push(p)
		if err != nil {
			return nil, err
		}
		ordered = append(ordered, out...)
	}
	ordered = append(ordered, buf.Flush()...)

	samples, mdat, err := parseSegmentSamples(segmentPath)
	if err != nil {
		return nil, err
	}
	sampleBytes, mdatTruncated := extractFrameBytes(samples, mdat)

	// Reassemble frame payloads at RTP marker boundaries.
	framePayloads := reassembleFrames(ordered)

	rec := &SegmentRecovery{
		Spec: spec, Seq: seq,
		LostPackets:   buf.LostCount,
		DuplicatePkts: buf.DupCount,
		Truncated:     mdatTruncated,
	}
	// Physical mdat end: samples whose declared bytes start at or beyond
	// this offset were cut off by truncation and are undecodable even
	// though their RTP packets arrived intact.
	mdatEnd := len(mdat)
	sampleStart := 0
	for idx := range samples {
		f := media.Frame{
			TrackID: spec.ID, SegmentSeq: seq, Index: uint32(idx),
			Width: spec.Width, Height: spec.Height,
			Duration: 1, // native frame units (30 fps timebase)
		}
		sampleLen := 0
		if idx < len(sampleBytes) {
			sampleLen = len(sampleBytes[idx])
		}
		physicallyCut := mdatTruncated && sampleStart >= mdatEnd
		var payload []byte
		if idx < len(framePayloads) {
			payload = framePayloads[idx]
		}
		switch {
		case physicallyCut:
			// Truncation removed this sample's container bytes.
			f.Corrupt = true
		case len(payload) == 0 || (sampleLen > 0 && len(payload) < sampleLen):
			// Missing due to burst packet loss.
			f.Corrupt = true
		default:
			arch, err := fixture.ParseArchFrame(payload)
			if err != nil {
				f.Corrupt = true
			} else {
				f.Luma = arch.Luma
				f.PayloadSize = arch.Size
			}
		}
		if idx < len(sampleBytes) {
			rec.MediaBytes += len(sampleBytes[idx])
		}
		sampleStart += sampleLen
		rec.Frames = append(rec.Frames, f)
	}
	return rec, nil
}

// reassembleFrames concatenates ordered packet payloads, splitting on
// the RTP marker bit which flags the last packet of each frame.
func reassembleFrames(ordered []*rtp.Packet) [][]byte {
	var frames [][]byte
	var cur []byte
	for _, a := range ordered {
		cur = append(cur, a.Payload...)
		if a.Marker {
			frames = append(frames, cur)
			cur = nil
		}
	}
	if len(cur) > 0 {
		frames = append(frames, cur)
	}
	return frames
}

// blockingError converts recovery counters into the locatable error that
// the DAG surfaces as the first blocking dependency. Packet loss blocks
// downstream composition; truncation does not block because its corrupt
// frames must still flow into the quality statistics and frame export.
func (r *SegmentRecovery) blockingError() error {
	if r.LostPackets > 0 {
		return merr.New(merr.KindTransport, "transport",
			"track:"+itoa(uint64(r.Spec.ID))+":seq:"+itoa(uint64(r.Seq)),
			"burst loss: "+itoa(uint64(r.LostPackets))+
				" packet(s) outside the reorder window")
	}
	return nil
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }
