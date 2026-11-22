// Package ingest validates self-generated frame sequences and indexes
// MP4 sample tables on the shared time base.
package ingest

import (
	"fmt"
	"image"
	"image/color"
	"math/rand"

	"avcheck/internal/media"
)

// GenConfig parameterizes deterministic synthetic capture.
type GenConfig struct {
	Seed          int64
	Frames        int // frames per track
	Tracks        int
	Width, Height int
	StartFrameSeq uint64 // first frame sequence number (non-zero allowed)
	StartTicks    int64  // base capture ticks; track i adds i*TrackTickSkew
	TrackTickSkew int64
	TicksPerFrame int64
	KeyInterval   int
}

// Pixel renders the deterministic RGBA pattern for one frame.
func Pixel(cfg GenConfig, track int, seq uint64, x, y int) color.RGBA {
	v := uint32(cfg.Seed)*2654435761 ^ uint32(track)*40503 ^ uint32(seq)*2246822519 ^ uint32(x)*3266489917 ^ uint32(y)*668265263
	v ^= v >> 15
	return color.RGBA{R: uint8(v), G: uint8(v >> 8), B: uint8(v >> 16), A: 255}
}

// Image renders a full deterministic frame image.
func Image(cfg GenConfig, track int, seq uint64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
	for y := 0; y < cfg.Height; y++ {
		for x := 0; x < cfg.Width; x++ {
			img.SetRGBA(x, y, Pixel(cfg, track, seq, x, y))
		}
	}
	return img
}

// Generate builds all tracks deterministically from the seed.
func Generate(cfg GenConfig) ([][]media.Frame, error) {
	if cfg.Frames <= 0 || cfg.Tracks <= 0 || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, media.NewError(media.KindRange, 0, 0, -1,
			"bad gen config frames=%d tracks=%d %dx%d", cfg.Frames, cfg.Tracks, cfg.Width, cfg.Height)
	}
	if cfg.KeyInterval < 1 {
		cfg.KeyInterval = 25
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	tracks := make([][]media.Frame, cfg.Tracks)
	for ti := range tracks {
		jitter := rng.Int63n(cfg.TicksPerFrame) // per-track random phase
		frames := make([]media.Frame, cfg.Frames)
		for i := 0; i < cfg.Frames; i++ {
			seq := cfg.StartFrameSeq + uint64(i)
			img := Image(cfg, ti, seq)
			f := media.Frame{
				StreamID: uint32(ti + 1),
				Seq:      seq,
				Ticks:    cfg.StartTicks + int64(ti)*cfg.TrackTickSkew + jitter + int64(i)*cfg.TicksPerFrame,
				Key:      i%cfg.KeyInterval == 0,
				Meta:     media.Meta{Version: media.MetaVersion, Length: uint32(len(img.Pix))},
				Payload:  img.Pix,
			}
			f.Sum = f.ComputeSum()
			frames[i] = f
		}
		tracks[ti] = frames
	}
	return tracks, nil
}

// Policy decides what a stage does with a corrupt frame.
type Policy uint8

const (
	PolicySkip      Policy = iota // reject the frame, keep the track alive
	PolicyFailTrack               // reject the frame and fail the whole track
)

// Validator checks frame length, metadata version and SHA-256.
type Validator struct {
	Policy       Policy
	Rejected     uint64
	FailedTracks map[uint32]bool
}

// NewValidator builds a validator with the given policy.
func NewValidator(p Policy) *Validator {
	return &Validator{Policy: p, FailedTracks: map[uint32]bool{}}
}

// Check validates one frame; on failure the error carries track and seq.
func (v *Validator) Check(f *media.Frame) error {
	fail := func(err error) error {
		v.Rejected++
		if v.Policy == PolicyFailTrack {
			v.FailedTracks[f.StreamID] = true
		}
		return err
	}
	if v.FailedTracks[f.StreamID] {
		return media.NewError(media.KindState, f.StreamID, f.Seq, -1, "track already failed")
	}
	if f.Meta.Version != media.MetaVersion {
		return fail(media.NewError(media.KindCorrupt, f.StreamID, f.Seq, -1,
			"metadata version %d, want %d", f.Meta.Version, media.MetaVersion))
	}
	if got, want := len(f.Payload), int(f.Meta.Length); got != want {
		kind := media.KindCorrupt
		if got < want {
			kind = media.KindTruncated
		}
		return fail(media.NewError(kind, f.StreamID, f.Seq, int64(got),
			"payload length %d, declared %d", got, want))
	}
	if sum := f.ComputeSum(); sum != f.Sum {
		return fail(media.NewError(media.KindChecksum, f.StreamID, f.Seq, -1,
			"sha256 %x..., want %x...", sum[:4], f.Sum[:4]))
	}
	return nil
}

func (p Policy) String() string {
	if p == PolicyFailTrack {
		return "fail-track"
	}
	return fmt.Sprint("skip")
}
