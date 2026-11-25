package stream

import (
	"context"
	"math"
	"sort"
	"sync"

	"github.com/pion/rtp"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
)

// Window is the expected shared-clock interval of one segment.
type Window struct {
	Index int         `json:"index"`
	Start clock.Ticks `json:"start"`
	End   clock.Ticks `json:"end"`
}

// Segment is a representation fragment boundary and its RTP evidence.
type Segment struct {
	Layer      string       `json:"layer"`
	Index      int          `json:"index"`
	Start      clock.Ticks  `json:"start"`
	End        clock.Ticks  `json:"end"`
	ByteOffset int64        `json:"byte_offset"`
	SHA256     string       `json:"sha256"`
	Packets    []rtp.Packet `json:"-"`
	Sequence   []uint16     `json:"sequences,omitempty"`
	Timestamps []uint32     `json:"timestamps,omitempty"`
}

// Verification is the quantitative fan-in result.
type Verification struct {
	LayerCount        int     `json:"layer_count"`
	SegmentCount      int     `json:"segment_count"`
	AlignmentErrorMS  float64 `json:"alignment_error_ms"`
	MaxTickError      int64   `json:"max_tick_error"`
	ValidatedSegments int     `json:"validated_segments"`
}

// ValidateRTPPackets checks duplicate sequence numbers and timestamp
// regression while locating the representation, segment, and packet offset.
func ValidateRTPPackets(layer string, segment int, packets []rtp.Packet) error {
	seenSeq := make(map[uint16]int)
	var prevTS uint32
	haveTS := false
	for i, pkt := range packets {
		if prev, dup := seenSeq[pkt.SequenceNumber]; dup {
			return fault.InLayer(fault.DuplicateSequence, layer, segment, int64(i),
				"duplicate RTP sequence %d at packet %d (first at %d)",
				pkt.SequenceNumber, i, prev)
		}
		seenSeq[pkt.SequenceNumber] = i
		if haveTS {
			// int32 subtraction also accepts a legitimate 32-bit wrap-forward.
			if int32(pkt.Timestamp-prevTS) < 0 {
				return fault.InLayer(fault.TimestampRegression, layer, segment, int64(i),
					"RTP timestamp regressed %d -> %d at packet %d", prevTS, pkt.Timestamp, i)
			}
		}
		prevTS = pkt.Timestamp
		haveTS = true
	}
	return nil
}

// Verify checks presence, RTP order, and shared-clock boundaries. Tolerance is
// one output frame duration.
func Verify(layers []string, windows []Window, segments []Segment, tolerance clock.Ticks) (*Verification, error) {
	if tolerance <= 0 {
		tolerance = 1
	}
	byLayer := map[string]map[int]Segment{}
	for _, name := range layers {
		byLayer[name] = map[int]Segment{}
	}
	for _, seg := range segments {
		if _, ok := byLayer[seg.Layer]; !ok {
			byLayer[seg.Layer] = map[int]Segment{}
		}
		byLayer[seg.Layer][seg.Index] = seg
	}

	res := &Verification{LayerCount: len(layers), SegmentCount: len(windows)}
	var maxErr clock.Ticks
	for _, name := range layers {
		got := byLayer[name]
		for _, w := range windows {
			seg, ok := got[w.Index]
			segmentOneBased := w.Index + 1
			if !ok {
				return res, fault.InLayer(fault.MissingSegment, name, segmentOneBased, 0,
					"segment %d missing", segmentOneBased)
			}
			if err := ValidateRTPPackets(name, segmentOneBased, seg.Packets); err != nil {
				return res, err
			}
			startErr := absTicks(seg.Start - w.Start)
			endErr := absTicks(seg.End - w.End)
			errTicks := startErr
			if endErr > errTicks {
				errTicks = endErr
			}
			if errTicks > maxErr {
				maxErr = errTicks
			}
			if errTicks > tolerance {
				return res, fault.InLayer(fault.BoundaryMismatch, name, segmentOneBased, int64(errTicks),
					"boundary error %.3f ms exceeds one frame %.3f ms (start=%d/%d end=%d/%d)",
					errTicks.Milliseconds(), tolerance.Milliseconds(),
					seg.Start, w.Start, seg.End, w.End)
			}
			res.ValidatedSegments++
		}
	}
	res.MaxTickError = int64(maxErr)
	res.AlignmentErrorMS = roundMS(maxErr.Milliseconds())
	return res, nil
}

func absTicks(t clock.Ticks) clock.Ticks {
	if t < 0 {
		return -t
	}
	return t
}

func roundMS(v float64) float64 {
	return math.Round(v*1000) / 1000
}

// LayerStream carries completed segments from one representation.
type LayerStream struct {
	Name     string
	Segments <-chan Segment
}

// FanIn collects layer channels and verifies them against the schedule.
func FanIn(ctx context.Context, streams []LayerStream, windows []Window, tolerance clock.Ticks) (*Verification, []Segment, error) {
	var mu sync.Mutex
	var all []Segment
	var wg sync.WaitGroup
	for _, ls := range streams {
		wg.Add(1)
		go func(ls LayerStream) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case seg, ok := <-ls.Segments:
					if !ok {
						return
					}
					mu.Lock()
					all = append(all, seg)
					mu.Unlock()
				}
			}
		}(ls)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, sortedSegments(all), fault.Wrap(fault.QueueCanceled, ctx.Err(), "fan-in canceled")
	}
	layers := make([]string, 0, len(streams))
	for _, s := range streams {
		layers = append(layers, s.Name)
	}
	sort.Strings(layers)
	ver, err := Verify(layers, windows, all, tolerance)
	return ver, sortedSegments(all), err
}

func sortedSegments(in []Segment) []Segment {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Layer != in[j].Layer {
			return in[i].Layer < in[j].Layer
		}
		return in[i].Index < in[j].Index
	})
	return in
}
