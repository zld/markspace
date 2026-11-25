package fixture

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"github.com/zld/media-check/internal/mp4"
)

const (
	CorruptTruncatedBox        = "truncated-box"
	CorruptMissingKeyframe     = "missing-keyframe"
	CorruptTimestampRegression = "timestamp-regression"
)

type Config struct {
	Seed    int64
	Corrupt string
}

type Fixture struct {
	Config    Config
	Manifest  *mp4.Manifest
	ValidData []byte
	Data      []byte
	Frames    []Frame
	Audio     []AudioBlockStats
	Peak      int
	PeakDBFS  float64
}

// Generate builds all synthetic bytes in memory. The result is deterministic
// for a fixed Config.
func Generate(cfg Config) (*Fixture, error) {
	if cfg.Seed == 0 {
		cfg.Seed = 17
	}
	switch cfg.Corrupt {
	case "", CorruptTruncatedBox, CorruptMissingKeyframe, CorruptTimestampRegression:
	default:
		return nil, fmt.Errorf("unsupported corruption %q", cfg.Corrupt)
	}

	frames := make([]Frame, FrameCount)
	for i := 0; i < FrameCount; i++ {
		seg := i / 3
		local := i % 3
		material := []string{"mat-a", "mat-b", "mat-c", "mat-b"}[seg]
		frames[i] = GenerateFrame(cfg.Seed, material, local, i)
	}
	audio := GenerateAudio(cfg.Seed)
	peak := 0
	for _, b := range audio {
		if b.Peak > peak {
			peak = b.Peak
		}
	}

	tracks := []mp4.Track{
		{TrackID: 1, Kind: "video", Timescale: VideoTimescale, Duration: FrameCount * FrameDuration, Width: FrameWidth, Height: FrameHeight},
		{TrackID: 2, Kind: "audio", Timescale: AudioTimescale, Duration: FrameCount * AudioBlock, Channels: AudioChannels, SampleRate: AudioTimescale},
		{TrackID: 3, Kind: "subtitle", Timescale: 1000, Duration: 1200},
	}

	mdat := []byte{}
	samples := []mp4.Sample{}
	addPacket := func(trackID uint32, index int, pts, dts, duration int64, sync bool, material string, payload []byte) error {
		pt := uint8(96)
		ssrc := uint32(0x01000000 + trackID)
		if trackID == 2 {
			pt = 97
		} else if trackID == 3 {
			pt = 98
		}
		marker := index%3 == 2
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    pt,
				SequenceNumber: uint16(index + 1),
				Timestamp:      uint32(pts),
				SSRC:           ssrc,
				Marker:         marker,
			},
			Payload: payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return err
		}
		samples = append(samples, mp4.Sample{
			TrackID:      trackID,
			Index:        index,
			PTS:          pts,
			DTS:          dts,
			Duration:     duration,
			IsSync:       sync,
			Size:         len(raw),
			MdatOffset:   int64(len(mdat)),
			RTPSequence:  uint16(index + 1),
			RTPTimestamp: uint32(pts),
			MaterialID:   material,
		})
		mdat = append(mdat, raw...)
		return nil
	}

	for i, frame := range frames {
		pts := int64(i) * FrameDuration
		dts := pts
		sync := i%3 == 0
		if cfg.Corrupt == CorruptMissingKeyframe && i == 3 {
			sync = false
		}
		if cfg.Corrupt == CorruptTimestampRegression && i == 5 {
			pts = 4*FrameDuration - FrameDuration/2 // 350 ms, before sample 4's 400 ms
			dts = pts
		}
		seg := i / 3
		material := []string{"mat-a", "mat-b", "mat-c", "mat-b"}[seg]
		if err := addPacket(1, i, pts, dts, FrameDuration, sync, material, frame.RGB); err != nil {
			return nil, err
		}
	}

	for i, block := range audio {
		raw := make([]byte, len(block.PCM)*2)
		for j, v := range block.PCM {
			binary.LittleEndian.PutUint16(raw[j*2:], uint16(v))
		}
		pts := int64(i) * AudioBlock
		if err := addPacket(2, i, pts, pts, AudioBlock, true, "", raw); err != nil {
			return nil, err
		}
	}

	subtitles := []string{"intro", "clip-b", "clip-c", "outro"}
	for i, text := range subtitles {
		pts := int64(i) * 300
		if err := addPacket(3, i, pts, pts, 300, true, "", []byte(text)); err != nil {
			return nil, err
		}
	}

	manifest := &mp4.Manifest{
		Seed:          cfg.Seed,
		TimelineScale: mp4.TimelineScale,
		Tracks:        tracks,
		Samples:       samples,
		Segments: []mp4.Segment{
			{ID: "seg-1", TrackID: 1, MaterialID: "mat-a", TimelineStart: 0, TimelineEnd: 300, FirstSampleIndex: 0, LastSampleIndex: 2},
			{ID: "seg-2", TrackID: 1, MaterialID: "mat-b", TimelineStart: 250, TimelineEnd: 550, FirstSampleIndex: 3, LastSampleIndex: 5},
			{ID: "seg-3", TrackID: 1, MaterialID: "mat-c", TimelineStart: 500, TimelineEnd: 800, FirstSampleIndex: 6, LastSampleIndex: 8},
			{ID: "seg-4", TrackID: 1, MaterialID: "mat-b", TimelineStart: 750, TimelineEnd: 1050, FirstSampleIndex: 9, LastSampleIndex: 11},
		},
		Transitions: []mp4.Transition{
			{ID: "tr-1", Kind: "dissolve", FromSegmentID: "seg-1", ToSegmentID: "seg-2", StartMS: 250, EndMS: 300},
			{ID: "tr-2", Kind: "wipe", FromSegmentID: "seg-2", ToSegmentID: "seg-3", StartMS: 500, EndMS: 550},
			{ID: "tr-3", Kind: "dissolve", FromSegmentID: "seg-3", ToSegmentID: "seg-4", StartMS: 750, EndMS: 800},
		},
		Edits: []mp4.Edit{
			{ID: "edit-0", TrackID: 1, Kind: "cut", AtMS: 0},
			{ID: "edit-1", TrackID: 1, Kind: "cut", AtMS: 275},
			{ID: "edit-2", TrackID: 1, Kind: "cut", AtMS: 525},
			{ID: "edit-3", TrackID: 1, Kind: "cut", AtMS: 775},
		},
	}

	valid, err := mp4.BuildFile(manifest, mdat)
	if err != nil {
		return nil, err
	}
	f := &Fixture{
		Config:    cfg,
		Manifest:  manifest,
		ValidData: valid,
		Frames:    frames,
		Audio:     audio,
		Peak:      peak,
		PeakDBFS:  PeakDBFS(peak),
	}

	if cfg.Corrupt == CorruptTruncatedBox {
		parsed := mp4.Parse(valid, "evidence", "")
		if parsed.Container.Mdat == nil || parsed.Container.Manifest == nil {
			return nil, fmt.Errorf("valid fixture failed self-parse")
		}
		var target *mp4.Sample
		for i := range manifest.Samples {
			s := &manifest.Samples[i]
			if s.TrackID == 1 && s.Index == 7 {
				target = s
				break
			}
		}
		if target == nil {
			return nil, fmt.Errorf("truncation target missing")
		}
		abs := parsed.Container.Mdat.PayloadOffset + target.MdatOffset
		cut := abs + int64(target.Size)/2
		if cut <= parsed.Container.Mdat.PayloadOffset || cut >= int64(len(valid)) {
			return nil, fmt.Errorf("invalid truncation point %d", cut)
		}
		f.Data = append([]byte(nil), valid[:cut]...)
	} else {
		f.Data = valid
	}
	return f, nil
}

// WriteSet writes the active input and an unmodified evidence copy.
func WriteSet(root string, f *Fixture) (inputPath, evidencePath string, err error) {
	inputDir := filepath.Join(root, "input")
	evidenceDir := filepath.Join(root, "evidence")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(evidenceDir, 0o755); err != nil {
		return "", "", err
	}
	inputPath = filepath.Join(inputDir, "media-001.mp4")
	evidencePath = filepath.Join(evidenceDir, "media-001.valid.mp4")
	if err := os.WriteFile(inputPath, f.Data, 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(evidencePath, f.ValidData, 0o644); err != nil {
		return "", "", err
	}
	return inputPath, evidencePath, nil
}
