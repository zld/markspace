package fixture

import (
	"math"
	"os"
	"path/filepath"

	"replaycheck/internal/clock"
)

// Sample kinds understood by the batch adapter.
const (
	KindNormal       = "mp4_normal"
	KindTruncated    = "mp4_truncated_header"
	KindRTPWrap      = "rtp_wrap"
	KindVFR          = "mp4_vfr"
	KindChunkCorrupt = "rtp_chunk_corrupt"
)

// Sample is one generated acceptance case and its intended event interval.
type Sample struct {
	Name                 string `json:"name"`
	Path                 string `json:"path"`
	Kind                 string `json:"kind"`
	InputDurationNS      int64  `json:"input_duration_ns"`
	EventRelativeStartNS int64  `json:"event_relative_start_ns"`
	EventRelativeEndNS   int64  `json:"event_relative_end_ns"`
	ChunkCount           int    `json:"chunk_count"`
	ExpectFailure        bool   `json:"expect_failure"`
}

// Set is the generated five-file self-test fixture set.
type Set struct {
	Dir     string   `json:"dir"`
	Samples []Sample `json:"samples"`
}

// GenerateSet creates all five required samples under dir.
func GenerateSet(dir string) (*Set, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	set := &Set{Dir: dir}

	normalBytes := BuildMP4(MP4Spec{
		MovieTimescale: 1000,
		MovieDuration:  2000,
		Tracks: []TrackSpec{
			{TrackID: 1, Timescale: 90000, Duration: 180045, Deltas: repeatDelta(60, 3000)},
			{TrackID: 2, Timescale: 48000, Duration: 96024, Deltas: []uint32{48000, 48000}},
		},
	})
	if err := os.WriteFile(filepath.Join(dir, "01_normal.mp4"), normalBytes, 0o644); err != nil {
		return nil, err
	}
	set.Samples = append(set.Samples, Sample{
		Name: "normal", Path: filepath.Join(dir, "01_normal.mp4"), Kind: KindNormal,
		InputDurationNS:      2 * clock.NanosecondsPerSecond,
		EventRelativeStartNS: 500 * clock.NanosecondsPerMillisecond,
		EventRelativeEndNS:   1000 * clock.NanosecondsPerMillisecond,
		ChunkCount:           2,
	})

	truncated := normalBytes[:40]
	truncPath := filepath.Join(dir, "02_truncated_header.mp4")
	if err := os.WriteFile(truncPath, truncated, 0o644); err != nil {
		return nil, err
	}
	set.Samples = append(set.Samples, Sample{
		Name: "truncated_header", Path: truncPath, Kind: KindTruncated,
		InputDurationNS:      2 * clock.NanosecondsPerSecond,
		EventRelativeStartNS: 500 * clock.NanosecondsPerMillisecond,
		EventRelativeEndNS:   1000 * clock.NanosecondsPerMillisecond,
		ChunkCount:           1, ExpectFailure: true,
	})

	wrapRecords := []RTPRecordSpec{}
	wrapStart := uint32(math.MaxUint32 - 6000)
	for i := 0; i < 6; i++ {
		wrapRecords = append(wrapRecords, RTPRecordSpec{
			Sequence: uint16(10 + i), Timestamp: wrapStart + uint32(i*3000),
			SSRC: 0x52545031, Payload: []byte{byte(i), 0x01, 0x02, 0x03},
		})
	}
	wrapBytes, _, err := BuildRTPFile(wrapRecords)
	if err != nil {
		return nil, err
	}
	wrapPath := filepath.Join(dir, "03_rtp_wrap.rtpfix")
	if err := os.WriteFile(wrapPath, wrapBytes, 0o644); err != nil {
		return nil, err
	}
	set.Samples = append(set.Samples, Sample{
		Name: "timestamp_wrap", Path: wrapPath, Kind: KindRTPWrap,
		InputDurationNS:      15_000 * clock.NanosecondsPerSecond / 90_000,
		EventRelativeStartNS: 80 * clock.NanosecondsPerMillisecond,
		EventRelativeEndNS:   120 * clock.NanosecondsPerMillisecond,
		ChunkCount:           2,
	})

	vfrDeltas := []uint32{1500, 4500, 3000, 1500, 4500, 3000}
	vfrBytes := BuildMP4(MP4Spec{
		MovieTimescale: 1000,
		MovieDuration:  200,
		Tracks: []TrackSpec{{
			TrackID: 1, Timescale: 90000, Duration: 18000, Deltas: vfrDeltas,
		}},
	})
	vfrPath := filepath.Join(dir, "04_vfr.mp4")
	if err := os.WriteFile(vfrPath, vfrBytes, 0o644); err != nil {
		return nil, err
	}
	vfrStart, _ := clock.Convert(2500, 90000)
	vfrEnd, _ := clock.Convert(10000, 90000)
	set.Samples = append(set.Samples, Sample{
		Name: "variable_frame_rate", Path: vfrPath, Kind: KindVFR,
		InputDurationNS:      200 * clock.NanosecondsPerMillisecond,
		EventRelativeStartNS: vfrStart, EventRelativeEndNS: vfrEnd,
		ChunkCount: 2,
	})

	corruptRecords := []RTPRecordSpec{}
	corruptStart := uint32(math.MaxUint32 - 9000)
	for i := 0; i < 4; i++ {
		corruptRecords = append(corruptRecords, RTPRecordSpec{
			Sequence: uint16(20 + i), Timestamp: corruptStart + uint32(i*3000),
			SSRC: 0x434f5252, Payload: []byte{byte(i), 0xaa},
		})
	}
	corruptRecords = append(corruptRecords, RTPRecordSpec{
		Sequence: 24, Timestamp: 2999, SSRC: 0x434f5252,
		// Unsupported RTP version and deliberately invalid header.
		Raw: []byte{0x10, 0x60, 0x00, 0x18, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0xde, 0xad},
	})
	corruptBytes, _, err := BuildRTPFile(corruptRecords)
	if err != nil {
		return nil, err
	}
	corruptPath := filepath.Join(dir, "05_chunk_corrupt.rtpfix")
	if err := os.WriteFile(corruptPath, corruptBytes, 0o644); err != nil {
		return nil, err
	}
	set.Samples = append(set.Samples, Sample{
		Name: "chunk_boundary_corruption", Path: corruptPath, Kind: KindChunkCorrupt,
		InputDurationNS:      100 * clock.NanosecondsPerMillisecond,
		EventRelativeStartNS: 60 * clock.NanosecondsPerMillisecond,
		EventRelativeEndNS:   100 * clock.NanosecondsPerMillisecond,
		ChunkCount:           1, ExpectFailure: true,
	})

	return set, nil
}

func repeatDelta(n int, delta uint32) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = delta
	}
	return out
}
