package sample

import (
	"fmt"
	"strings"

	"mediapack/model"
)

const (
	DurationNanos      int64 = 12_000_000_000
	FrameIntervalNanos int64 = 40_000_000 // 25 fps, exact 40 ms grid
	KeyframeIntervalNS int64 = 2_000_000_000
	FrameCount         int   = int(DurationNanos / FrameIntervalNanos)
)

// Representation is one generated bitrate ladder entry.
type Representation struct {
	ID      string
	Track   model.TrackID
	Bitrate int
	Width   int
	Height  int
}

// Sample is the deterministic local source used by the terminal demo.
type Sample struct {
	Seed          int64
	DurationNanos int64
	Reps          []Representation
	Keyframes     []int64
	Video         [][]*model.Unit
	Audio         []*model.Unit
	Metadata      []byte
}

// Bitrates returns the deterministic ladder for count representations.
func Bitrates(count int) []Representation {
	bitrates := []int{2_400_000, 1_200_000, 600_000, 300_000}
	res := make([]Representation, 0, count)
	for i := 0; i < count; i++ {
		height := []int{1080, 720, 480, 360}[i]
		width := height * 16 / 9
		res = append(res, Representation{
			ID:      fmt.Sprintf("rep-%d", i),
			Track:   model.TrackID(fmt.Sprintf("video-%d", i)),
			Bitrate: bitrates[i],
			Width:   width,
			Height:  height,
		})
	}
	return res
}

// Generate builds deterministic multi-representation video, audio, and metadata.
func Generate(seed int64, bitrateCount int) *Sample {
	reps := Bitrates(bitrateCount)
	s := &Sample{Seed: seed, DurationNanos: DurationNanos, Reps: reps}
	for t := int64(0); t < DurationNanos; t += KeyframeIntervalNS {
		s.Keyframes = append(s.Keyframes, t)
	}

	s.Video = make([][]*model.Unit, len(reps))
	for r, rep := range reps {
		s.Video[r] = make([]*model.Unit, 0, FrameCount)
		for i := 0; i < FrameCount; i++ {
			pts := int64(i) * FrameIntervalNanos
			keyframe := pts%KeyframeIntervalNS == 0
			payload := deterministicPayload(seed, r, i, 192)
			s.Video[r] = append(s.Video[r], &model.Unit{
				ID: fmt.Sprintf("%s-frame-%04d", rep.ID, i), Track: rep.Track,
				Representation: rep.ID, Kind: model.KindVideo, Index: i,
				Sequence: uint16(i), PTSNanos: pts, DTSNanos: pts,
				DurationNanos: FrameIntervalNanos, Keyframe: keyframe,
				SourceOffset: int64(8 + i*224), Size: len(payload), Payload: payload,
			})
		}
	}

	// A fixed 4 ms offset is deliberately within the 20 ms acceptance bound.
	const audioOffset = int64(4_000_000)
	s.Audio = make([]*model.Unit, 0, FrameCount)
	for i := 0; i < FrameCount; i++ {
		pts := int64(i)*FrameIntervalNanos + audioOffset
		payload := deterministicPayload(seed, 99, i, 64)
		s.Audio = append(s.Audio, &model.Unit{
			ID: fmt.Sprintf("audio-frame-%04d", i), Track: "audio",
			Kind: model.KindAudio, Index: i, Sequence: uint16(i),
			PTSNanos: pts, DTSNanos: pts, DurationNanos: FrameIntervalNanos,
			SourceOffset: int64(i * 80), Size: len(payload), Payload: payload,
		})
	}
	s.Metadata = buildMetadataFixture()
	return s
}

func deterministicPayload(seed int64, salt, index, size int) []byte {
	b := make([]byte, size)
	x := uint64(seed)*1_000_003 + uint64(salt)*7919 + uint64(index)*31 + 1
	for i := range b {
		x = x*6364136223846793005 + 1442695040888963407
		b[i] = byte(x >> 33)
	}
	return b
}

func buildMetadataFixture() []byte {
	var b strings.Builder
	b.WriteString("# generated mediapack metadata fixture\n")
	for i := 0; i < 6; i++ {
		start := int64(i) * 2_000_000_000
		end := start + 1_800_000_000
		fmt.Fprintf(&b, "SUB\t1/1000000000\t%d\t%d\t字幕线索 %d\n", start, end, i+1)
	}
	for i, title := range []string{"开场", "主体", "结尾"} {
		start := int64(i) * 4_000_000_000
		fmt.Fprintf(&b, "CHAP\t1/1000\t%d\t%d\t%s\n", start/1_000_000, (start+3_500_000_000)/1_000_000, title)
	}
	for i, ptsMS := range []int64{1_000, 5_000, 9_000} {
		fmt.Fprintf(&b, "ID3\t1/90000\t%d\tTIT2\t生成标题-%d\n", ptsMS*90, i+1)
	}
	for i, ptsMS := range []int64{2_000, 6_000, 10_000} {
		fmt.Fprintf(&b, "EMSG\t1/1000\t%d\t500\turn:mediapack:cue\tcue-%d\n", ptsMS, i+1)
	}
	for i, pts := range []int64{0, 4_000_000_000, 6_000_000_000, 10_000_000_000} {
		fmt.Fprintf(&b, "SEI\t1/90000\t%d\t5\t%02x%02x%02x%02x\n",
			pts*90_000/1_000_000_000, byte(i+1), byte(17), byte(0), byte(0))
	}
	return []byte(b.String())
}
