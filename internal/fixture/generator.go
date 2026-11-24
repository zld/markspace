// Package fixture generates every test and demo input locally: frame
// payloads, their manifest with hashes and timestamps, and PCM audio. No
// external files are referenced; all randomness is seeded so reruns produce
// byte-identical fixtures.
package fixture

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"

	"mediapipe/internal/model"
)

// Spec controls a generated delivery set.
type Spec struct {
	Seed        int64
	Frames      int
	AudioRate   int64
	Channels    int
	ColorTarget string
	// Corruption, when set, is injected into the video track.
	Corruption Corruption
}

// Corruption enumerates the fixture fault injectors.
type Corruption string

const (
	None           Corruption = ""
	Truncated      Corruption = "truncated"
	HashMismatch   Corruption = "hash_mismatch"
	TimestampBack  Corruption = "timestamp_back"
	UnknownColor   Corruption = "unknown_color"
)

// ManifestDTO types mirror the manifest package's on-disk schema.
type frameDTO struct {
	Index        int             `json:"index"`
	Name         string          `json:"name"`
	Length       int64           `json:"length"`
	SHA256       string          `json:"sha256"`
	TimeUnits    int64           `json:"time_units"`
	TimeRate     int64           `json:"time_rate"`
	Color        model.ColorMeta `json:"color"`
	Seq          uint16          `json:"rtp_seq"`
	RTPTimestamp uint32          `json:"rtp_timestamp"`
}

type trackDTO struct {
	ID               string     `json:"id"`
	Kind             model.Kind `json:"kind"`
	FPS              int64      `json:"fps,omitempty"`
	SampleRate       int64      `json:"sample_rate,omitempty"`
	Channels         int        `json:"channels,omitempty"`
	Layout           string     `json:"layout,omitempty"`
	StartOffsetUnits int64      `json:"start_offset_units,omitempty"`
	StartOffsetRate  int64      `json:"start_offset_rate,omitempty"`
	LoudnessLUFS     float64    `json:"loudness_lufs,omitempty"`
	Frames           []frameDTO `json:"frames"`
}

type fileDTO struct {
	Tracks []trackDTO `json:"tracks"`
}

// Audio is the generated PCM plus its parameters, handed to audio stages.
type Audio struct {
	PCM        []float64
	Rate       int64
	Channels   int
	SampleCount int
}

// Generated is the full fixture set written to a directory.
type Generated struct {
	Dir        string
	Manifest   fileDTO
	AudioTrack Audio
	// CorruptAt is the frame index carrying the injected fault, or -1.
	CorruptAt int
}

const frameBytes = 256

// Build materializes the fixture under dir and returns its description.
// Faults are injected at a deterministic frame near the middle so both
// pre-fault and post-fault scheduling paths are exercised.
func Build(dir string, spec Spec) (*Generated, error) {
	if spec.Frames <= 0 {
		return nil, fmt.Errorf("fixture: frames must be positive")
	}
	if spec.ColorTarget == "" {
		spec.ColorTarget = "bt709"
	}
	rng := rand.New(rand.NewSource(spec.Seed))
	videoDir := filepath.Join(dir, "vid-1")
	audioDir := filepath.Join(dir, "aud-1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(audioDir, 0o755); err != nil {
		return nil, err
	}

	corruptAt := spec.Frames * 2 / 3
	if spec.Corruption == None {
		corruptAt = -1
	}
	vid := trackDTO{ID: "vid-1", Kind: model.KindVideo, FPS: 30,
		StartOffsetUnits: 50, StartOffsetRate: 1000} // 50 ms non-zero start
	for i := 0; i < spec.Frames; i++ {
		payload := make([]byte, frameBytes)
		if _, err := rng.Read(payload); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(payload)
		declared := hex.EncodeToString(sum[:])
		name := fmt.Sprintf("f%05d.raw", i)
		written := append([]byte(nil), payload...)
		label := spec.ColorTarget
		switch {
		case i == corruptAt && spec.Corruption == Truncated:
			written = written[:frameBytes/2]
		case i == corruptAt && spec.Corruption == HashMismatch:
			declared = hex.EncodeToString(sha256.New().Sum(nil)) // all-zero digest
		case i == corruptAt && spec.Corruption == UnknownColor:
			label = "acme-gamut-9"
		}
		if err := os.WriteFile(filepath.Join(videoDir, name), written, 0o644); err != nil {
			return nil, err
		}
		// Deterministic per-frame 30 fps timing with +/-1-unit jitter.
		jitter := int64(0)
		if i%7 == 3 {
			jitter = 1
		}
		if i == corruptAt && spec.Corruption == TimestampBack {
			jitter = -9000 // large backward step
		}
		vid.Frames = append(vid.Frames, frameDTO{
			Index: i, Name: name, Length: int64(len(payload)), SHA256: declared,
			TimeUnits: int64(i) * 3000 + jitter, TimeRate: 90000,
			Color:        model.ColorMeta{Label: label, Source: "fixture"},
			Seq:          uint16(i),
			RTPTimestamp: uint32(i * 3000),
		})
	}

	// Audio: one second per frame count fraction, deterministic sine plus
	// occasional silence so loudness windows exercise both paths.
	samples := spec.Frames // 1 audio sample per video frame keeps the demo small
	aud := trackDTO{ID: "aud-1", Kind: model.KindAudio, SampleRate: spec.AudioRate,
		Channels: spec.Channels, Layout: layoutName(spec.Channels), LoudnessLUFS: -23}
	pcm := make([]float64, samples*spec.Channels)
	for n := 0; n < samples; n++ {
		v := 0.4 * sine(n, spec.AudioRate)
		if n%29 == 0 {
			v = 0 // periodic silent instants, not full windows
		}
		for c := 0; c < spec.Channels; c++ {
			pcm[n*spec.Channels+c] = v * (1 - 0.05*float64(c))
		}
	}
	for i := 0; i < samples; i++ {
		chunk := pcm[i*spec.Channels : (i+1)*spec.Channels]
		raw := floatBytes(chunk)
		sum := sha256.Sum256(raw)
		name := fmt.Sprintf("a%05d.raw", i)
		if err := os.WriteFile(filepath.Join(audioDir, name), raw, 0o644); err != nil {
			return nil, err
		}
		aud.Frames = append(aud.Frames, frameDTO{
			Index: i, Name: name,
			Length: int64(len(raw)), SHA256: hex.EncodeToString(sum[:]),
			TimeUnits: int64(i) * spec.AudioRate / 30, TimeRate: spec.AudioRate,
			Seq: uint16(i), RTPTimestamp: uint32(int64(i) * spec.AudioRate / 30),
		})
	}

	mf := fileDTO{Tracks: []trackDTO{vid, aud}}
	mb, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mb, 0o644); err != nil {
		return nil, err
	}
	return &Generated{
		Dir: dir, Manifest: mf, CorruptAt: corruptAt,
		AudioTrack: Audio{PCM: pcm, Rate: spec.AudioRate, Channels: spec.Channels, SampleCount: samples},
	}, nil
}

func layoutName(channels int) string {
	switch channels {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	default:
		return "unknown"
	}
}

// floatBytes is the deterministic little-endian byte representation used for
// audio-frame hashes in the manifest.
func floatBytes(pcm []float64) []byte {
	b := make([]byte, len(pcm)*8)
	for i, v := range pcm {
		binary.LittleEndian.PutUint64(b[i*8:], math.Float64bits(v))
	}
	return b
}

func sine(n int, rate int64) float64 {
	const hz = 440.0
	return math.Sin(2 * math.Pi * hz * float64(n) / float64(rate))
}
