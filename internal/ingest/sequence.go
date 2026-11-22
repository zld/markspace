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

// rowHash mixes the capture identity for one image row.
func rowHash(cfg GenConfig, track int, seq uint64, y int) uint32 {
	v := uint32(cfg.Seed)*2654435761 ^ uint32(track)*40503 ^ uint32(seq)*2246822519 ^ uint32(y)*3266489917
	v ^= v >> 15
	return v
}

// Pixel renders the deterministic RGBA value at (x,y). Image and Pixel share
// this formula, so a frame can be regenerated from its identity alone.
func Pixel(cfg GenConfig, track int, seq uint64, x, y int) color.RGBA {
	v := rowHash(cfg, track, seq, y)
	u := uint32(x) * 2654435761
	return color.RGBA{R: uint8(v) + uint8(u>>24), G: uint8(v>>8) + uint8(u>>16), B: uint8(v>>16) + uint8(u>>8), A: 255}
}

// Image renders a full deterministic frame image row by row.
func Image(cfg GenConfig, track int, seq uint64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
	step := cfg.Width
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return img
	}
	pix := img.Pix
	for y := 0; y < cfg.Height; y++ {
		v := rowHash(cfg, track, seq, y)
		row := pix[y*img.Stride : y*img.Stride+step*4]
		var u uint32
		for x := 0; x < step; x++ {
			row[x*4] = uint8(v) + uint8(u>>24)
			row[x*4+1] = uint8(v>>8) + uint8(u>>16)
			row[x*4+2] = uint8(v>>16) + uint8(u>>8)
			row[x*4+3] = 255
			u += 2654435761
		}
	}
	return img
}

// Placeholder renders the deterministic gap-filler image: a flat neutral
// field with a diagonal marker so a gap is visually obvious.
func Placeholder(width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+width*4]
		for x := 0; x < width; x++ {
			c := color.RGBA{R: 40, G: 40, B: 48, A: 255}
			if (x+y)%16 == 0 {
				c = color.RGBA{R: 200, G: 60, B: 60, A: 255}
			}
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = c.R, c.G, c.B, c.A
		}
	}
	return img
}

// FrameAt builds one frame on demand, deterministically from the seed. It is
// the streaming counterpart of Generate and keeps memory bounded.
func FrameAt(cfg GenConfig, track, index int) (*media.Frame, error) {
	if track < 0 || track >= cfg.Tracks || index < 0 || index >= cfg.Frames {
		return nil, media.NewError(media.KindRange, uint32(track+1), uint64(index), -1,
			"frame index outside generated range")
	}
	img := Image(cfg, track, cfg.StartFrameSeq+uint64(index))
	f := &media.Frame{
		StreamID: uint32(track + 1),
		Seq:      cfg.StartFrameSeq + uint64(index),
		Ticks:    cfg.StartTicks + int64(track)*cfg.TrackTickSkew + int64(index)*cfg.TicksPerFrame,
		Key:      index%cfg.KeyInterval == 0,
		Meta:     media.Meta{Version: media.MetaVersion, Length: uint32(len(img.Pix))},
		Payload:  img.Pix,
	}
	f.Sum = f.ComputeSum()
	return f, nil
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

	// Short aliases used by callers that would otherwise import this package
	// only to name a policy.
	Skip      = PolicySkip
	FailTrack = PolicyFailTrack
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
