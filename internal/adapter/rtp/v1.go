// Package rtp is the v1 RTP capture adapter. It parses length-framed
// packet dumps (4-byte big-endian length prefix per packet), reconstructs
// absolute media timestamps with RFC 3550 wrap handling, and distinguishes
// duplicates, ordinary fallback and unrecoverable wrap-around.
package rtp

import (
	"encoding/binary"
	"fmt"
	"os"

	pion "github.com/pion/rtp"

	"mediad/internal/adapter"
	"mediad/internal/model"
)

// Version is pinned in checkpoints and provenance records.
const Version = "rtp-v1"

// MaxWrapTicks is the largest single-step 32-bit jump treated as a legal
// wrap rather than an unrecoverable timestamp loss.
const MaxWrapTicks int64 = 1 << 31

var errZeroFrameDuration = fmt.Errorf("frame_duration_ticks must be non-zero")

type parser struct{}

func init() { adapter.Register(parser{}) }

func (parser) Format() string  { return "rtp" }
func (parser) Version() string { return Version }

// StreamMeta describes a capture fixture.
type StreamMeta struct {
	ClockRate uint32         `json:"clock_rate"`
	Kind      model.TrackKind `json:"kind"`
	FrameTicks uint32        `json:"frame_duration_ticks"`
}

func (parser) Parse(in adapter.MediaInput) (*model.MediaTrack, error) {
	loc := model.SourceLoc{
		Manifest: in.Manifest, Representation: in.Representation,
		Entry: in.Entry, Segment: in.Path, Offset: -1, Frame: -1,
	}
	meta, data, err := loadStream(in.Path)
	if err != nil {
		return nil, model.WrapError(model.KindCorruptBox, loc, err)
	}
	if meta.ClockRate == 0 {
		return nil, model.NewError(model.KindZeroTimescale, loc, "RTP clock rate is zero")
	}
	ts := model.TimeScale(meta.ClockRate)
	frames, err := parsePackets(data, meta, ts, loc)
	if err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, model.NewError(model.KindTruncatedFrame, loc, "capture contains no packets")
	}
	return &model.MediaTrack{
		ID: in.Representation, Kind: meta.Kind, TimeScale: ts,
		ClockRate: meta.ClockRate, Frames: frames,
		Source: model.SourceRef{
			Manifest: in.Manifest, Representation: in.Representation,
			Entry: in.Entry, File: in.Path,
		},
	}, nil
}

func parsePackets(data []byte, meta StreamMeta, ts model.TimeScale, loc model.SourceLoc) ([]model.Frame, error) {
	var frames []model.Frame
	var (
		haveBase bool
		absTick  int64 // cumulative, unwrapped, ticks
		lastRTP  uint32
		lastSeq  uint16
	)
	for i, pos := 0, 0; pos < len(data); i++ {
		pkt, next, err := readPacket(data, pos, i, loc)
		if err != nil {
			return nil, err
		}
		pos = next
		frameLoc := loc
		frameLoc.Frame = i
		if i > 0 {
			seqDiff := model.RTPSeqDiff(lastSeq, pkt.SequenceNumber)
			tsDiff := model.RTPTSDiff(lastRTP, pkt.Timestamp)
			if seqDiff == 0 && tsDiff == 0 {
				// Exact retransmission: drop, timeline unchanged.
				continue
			}
			// A sequence number that moves backwards is a genuine fallback;
			// a legal wrap keeps sequence numbers monotonically increasing.
			if seqDiff <= 0 {
				return nil, model.NewError(model.KindTimestampFallback, frameLoc,
					fmt.Sprintf("timestamp fallback: seq delta %d, ts delta %d", seqDiff, tsDiff))
			}
			if tsDiff < 0 {
				// Modular backward delta. Distinguish:
				//   fallback: clock really moved backwards by -tsDiff, a
				//             small step (< half a 32-bit cycle);
				//   wrap:     counter crossed zero, so the backward-looking
				//             distance is >= half a cycle, at which point
				//             wrap and fallback become ambiguous and we must
				//             not invent an index.
				backStep := int64(-tsDiff)
				if backStep < MaxWrapTicks {
					return nil, model.NewError(model.KindTimestampFallback, frameLoc,
						fmt.Sprintf("backward step %d ticks is not a wrap", backStep))
				}
				return nil, model.NewError(model.KindRTPWrapUnrecoverable, frameLoc,
					fmt.Sprintf("backward distance %d ticks at/beyond half-cycle boundary", backStep))
			}
		}
		if !haveBase {
			haveBase = true
			absTick = 0
		} else {
			step := model.RTPTSDiff(lastRTP, pkt.Timestamp)
			if step < 0 {
				step += 1 << 32 // validated wrap above
			}
			next := absTick + step
			if next < absTick {
				return nil, model.NewError(model.KindRTPWrapUnrecoverable, frameLoc,
					"absolute timestamp accumulation overflow")
			}
			absTick = next
		}
		pts, err := model.ScaleRound(absTick, ts)
		if err != nil {
			return nil, model.WrapError(model.KindFieldOutOfRange, frameLoc, err)
		}
		dur, err := model.ScaleRound(int64(meta.FrameTicks), ts)
		if err != nil {
			return nil, model.WrapError(model.KindFieldOutOfRange, frameLoc, err)
		}
		frames = append(frames, model.Frame{
			Kind: meta.Kind, PTS: pts, Dur: dur,
			KeyFrame: pkt.Marker && meta.Kind == model.TrackVideo,
			Payload:  append([]byte(nil), pkt.Payload...),
		})
		lastRTP, lastSeq = pkt.Timestamp, pkt.SequenceNumber
		pos = next
	}
	return frames, nil
}

// readPacket reads one 4-byte-length-prefixed packet at pos. A declared
// length extending past the capture end is a truncated-frame error located
// at the packet's byte offset.
func readPacket(data []byte, pos, idx int, loc model.SourceLoc) (*pion.Packet, int, error) {
	if pos+4 > len(data) {
		l := loc
		l.Offset = int64(pos)
		l.Frame = idx
		return nil, 0, model.NewError(model.KindTruncatedFrame, l,
			"capture ends mid length-prefix")
	}
	length := int(binary.BigEndian.Uint32(data[pos : pos+4]))
	if length <= 0 || pos+4+length > len(data) {
		l := loc
		l.Offset = int64(pos)
		l.Frame = idx
		return nil, 0, model.NewError(model.KindTruncatedFrame, l,
			fmt.Sprintf("declared packet length %d exceeds capture (%d bytes remain)",
				length, len(data)-pos-4))
	}
	pkt := &pion.Packet{}
	if err := pkt.Unmarshal(data[pos+4 : pos+4+length]); err != nil {
		l := loc
		l.Offset = int64(pos)
		l.Frame = idx
		return nil, 0, model.NewError(model.KindCorruptBox, l, "RTP unmarshal: "+err.Error())
	}
	return pkt, pos + 4 + length, nil
}

func loadStream(path string) (StreamMeta, []byte, error) {
	metaBytes, err := os.ReadFile(metaPath(path))
	if err != nil {
		return StreamMeta{}, nil, fmt.Errorf("missing stream meta: %w", err)
	}
	meta, err := decodeMeta(metaBytes)
	if err != nil {
		return StreamMeta{}, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return StreamMeta{}, nil, err
	}
	return meta, data, nil
}
