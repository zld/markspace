// Package fixture synthesizes deterministic MP4/MOV program files for the
// whole pipeline. No external media is referenced: every payload byte is
// derived from a seed so hashes stay reproducible.
package fixture

import "github.com/broadcast/mediad/internal/model"

// Corruption selects which defect a generated file carries.
type Corruption string

const (
	CorruptNone              Corruption = "none"
	CorruptTruncatedMdat     Corruption = "truncated_mdat"
	CorruptBackwardTimestamps Corruption = "backward_timestamps"
	CorruptSparseKeyframes   Corruption = "sparse_keyframes"
	CorruptNonZeroStart      Corruption = "nonzero_start"
	CorruptChannelConflict   Corruption = "channel_conflict"
	CorruptSampleRateConflict Corruption = "sample_rate_conflict"
	CorruptColorConflict     Corruption = "color_conflict"
)

// TrackSpec describes one synthesized track.
type TrackSpec struct {
	ID            uint32
	Kind          model.MediaKind
	Timescale     uint32
	SampleCount   int
	SampleSize    uint32
	KeyframeEvery int // video only; 0 means no stss box
	// StartUnits shifts the first decode timestamp (mdhd units), producing a
	// non-zero-start timeline with a leading gap.
	StartUnits uint32
	// Video fields
	Width, Height uint16
	// Audio fields
	ChannelCount uint16
	SampleRate   uint32
	// NCLX color triple; zero means "no colr box".
	ColorPrimaries, ColorTransfer, ColorMatrix uint16
}

// Spec is a complete fixture description.
type Spec struct {
	Name       string
	Seed       int64
	Corruption Corruption
	Tracks     []TrackSpec
}

// StandardSet returns the eight mandated fixtures. The seed controls payload
// bytes only; structure is fully determined by the specs.
func StandardSet(seed int64) []Spec {
	video := func() TrackSpec {
		return TrackSpec{
			ID: 1, Kind: model.KindVideo, Timescale: 24000, SampleCount: 48,
			SampleSize: 64, KeyframeEvery: 12, Width: 320, Height: 180,
			ColorPrimaries: 1, ColorTransfer: 1, ColorMatrix: 1,
		}
	}
	audio := func() TrackSpec {
		return TrackSpec{
			ID: 2, Kind: model.KindAudio, Timescale: 48000, SampleCount: 96,
			SampleSize: 32, ChannelCount: 2, SampleRate: 48000,
		}
	}
	return []Spec{
		{Name: "normal", Seed: seed, Corruption: CorruptNone, Tracks: []TrackSpec{video(), audio()}},
		{Name: "truncated_mdat", Seed: seed, Corruption: CorruptTruncatedMdat, Tracks: []TrackSpec{video(), audio()}},
		{Name: "backward_ts", Seed: seed, Corruption: CorruptBackwardTimestamps, Tracks: []TrackSpec{video(), audio()}},
		{Name: "sparse_kf", Seed: seed, Corruption: CorruptSparseKeyframes, Tracks: []TrackSpec{video(), audio()}},
		{Name: "nonzero_start", Seed: seed, Corruption: CorruptNonZeroStart,
			Tracks: []TrackSpec{withStart(video(), 12000), withStart(audio(), 24000)}},
		{Name: "channel_conflict", Seed: seed, Corruption: CorruptChannelConflict,
			Tracks: []TrackSpec{video(), withChannels(audio(), 6)}},
		{Name: "samplerate_conflict", Seed: seed, Corruption: CorruptSampleRateConflict,
			Tracks: []TrackSpec{video(), withSampleRate(audio(), 44100)}},
		{Name: "color_conflict", Seed: seed, Corruption: CorruptColorConflict,
			Tracks: []TrackSpec{withColor(video(), 9, 16, 9), audio()}},
	}
}

func withStart(t TrackSpec, units uint32) TrackSpec { t.StartUnits = units; return t }
func withChannels(t TrackSpec, c uint16) TrackSpec  { t.ChannelCount = c; return t }
func withSampleRate(t TrackSpec, r uint32) TrackSpec { t.SampleRate = r; return t }
func withColor(t TrackSpec, p, tr, m uint16) TrackSpec {
	t.ColorPrimaries, t.ColorTransfer, t.ColorMatrix = p, tr, m
	return t
}
