// Package render synthesizes offscreen frame sequences from the unified
// timeline. No windowing or GUI dependency is used: every frame is a PPM P6
// raster written to disk together with a per-frame provenance record and a
// SHA-256 manifest. Transitions deterministically blend the two video tracks.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediaforensics/internal/clock"
	"mediaforensics/internal/model"
)

// FrameWidth / FrameHeight define the synthetic raster. The small fixed
// size keeps frame output reviewable while the pipeline stays headless.
const (
	FrameWidth  = 64
	FrameHeight = 48
	MaxFrames   = 10000
)

// Provenance records exactly which source bytes produced one output frame.
type Provenance struct {
	Frame          int      `json:"frame"`
	OutputTime     string   `json:"output_time"` // rational seconds
	PrimaryTrack   int      `json:"primary_track"`
	PrimarySample  int      `json:"primary_sample"`
	PrimaryOffset  int64    `json:"primary_offset"`
	SecondaryTrack int      `json:"secondary_track"`
	SecondarySample int     `json:"secondary_sample"`
	SecondaryOffset int64   `json:"secondary_offset"`
	Transition     bool     `json:"transition"`
	BlendWeightA   int      `json:"blend_weight_a"` // 0..256
	Gap            bool     `json:"gap"`
	GapReason      string   `json:"gap_reason,omitempty"`
	SHA256         string   `json:"sha256"`
}

// FrameManifest is the on-disk checksum document for one render pass.
type FrameManifest struct {
	Width      int          `json:"width"`
	Height     int          `json:"height"`
	FrameCount int          `json:"frame_count"`
	GapCount   int          `json:"gap_count"`
	Frames     []Provenance `json:"frames"`
}

// Config controls a render pass.
type Config struct {
	OutputDir    string
	FrameCount   int
	FrameRateNum int64
	FrameRateDen int64 // output frame period = den/num seconds
	// TransitionEvery marks every Nth frame as a transition frame.
	TransitionEvery int
}

// Renderer consumes the timeline and emits frames plus the manifest.
type Renderer struct {
	cfg      Config
	placed   map[int][]model.ScheduledSample // track ID -> schedule
	tracks   map[int]*model.Track
}

// NewRenderer precomputes each track's scheduled placements.
func NewRenderer(tl *model.Timeline, cfg Config) (*Renderer, []*model.ForensicError) {
	if cfg.FrameCount > MaxFrames {
		return nil, []*model.ForensicError{model.NewError(model.StageRender, "frame_cap",
			-1, -1, "requested frames exceed 10000 limit")}
	}
	r := &Renderer{
		cfg:    cfg,
		placed: map[int][]model.ScheduledSample{},
		tracks: map[int]*model.Track{},
	}
	var errs []*model.ForensicError
	for _, tr := range tl.Tracks {
		r.tracks[tr.ID] = tr
		if tr.Kind != model.KindVideo {
			continue
		}
		placed, err := model.Schedule(tr)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		r.placed[tr.ID] = placed
	}
	return r, errs
}

// Render produces frameCount PPM files and returns the manifest. Frames whose
// source sample is missing or damaged are emitted as explicit gap frames so
// index/repair defects propagate into the frame sequence and its hashes.
func (r *Renderer) Render(frameCount int) (*FrameManifest, error) {
	if err := os.MkdirAll(r.cfg.OutputDir, 0o755); err != nil {
		return nil, err
	}
	manifest := &FrameManifest{Width: FrameWidth, Height: FrameHeight}
	videoIDs := r.sortedVideoIDs()
	for f := 0; f < frameCount; f++ {
		period := clock.New(r.cfg.FrameRateDen, r.cfg.FrameRateNum)
		at, err := period.Mul(clock.New(int64(f), 1))
		if err != nil {
			return nil, model.NewError(model.StageRender, "frame_time_overflow", -1, -1, err.Error())
		}
		prov, pixels, ferr := r.composeFrame(f, at, videoIDs)
		if ferr != nil && !prov.Gap {
			// A non-gap failure is fatal; gap frames are expected evidence.
			return nil, ferr
		}
		path := filepath.Join(r.cfg.OutputDir, frameName(f))
		if err := writePPM(path, pixels); err != nil {
			return nil, err
		}
		// The manifest checksum covers the complete PPM file including its
		// header, so reviewers can verify the artifact on disk byte-for-byte.
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		prov.SHA256 = hex.EncodeToString(sum[:])
		manifest.Frames = append(manifest.Frames, prov)
		if prov.Gap {
			manifest.GapCount++
		}
	}
	manifest.FrameCount = len(manifest.Frames)
	if err := r.writeManifest(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// composeFrame selects the sample active at time at on each video track and
// applies the deterministic transition blend.
func (r *Renderer) composeFrame(frameNo int, at clock.Rational, videoIDs []int) (Provenance, []byte, *model.ForensicError) {
	prov := Provenance{Frame: frameNo, OutputTime: at.String()}
	isTransition := r.cfg.TransitionEvery > 0 && frameNo > 0 &&
		frameNo%r.cfg.TransitionEvery == 0 && len(videoIDs) >= 2

	a, aOK := r.sampleAt(videoIDs[0], at)
	if !aOK {
		prov.Gap = true
		prov.GapReason = "no valid sample on primary video track"
		prov.PrimaryTrack = videoIDs[0]
		return prov, gapPixels(frameNo), model.NewError(model.StageRender, "frame_gap",
			videoIDs[0], r.bestOffset(videoIDs[0]), prov.GapReason)
	}
	prov.PrimaryTrack, prov.PrimarySample, prov.PrimaryOffset = a.TrackID, a.Index, a.Offset
	pixelsA := rasterFor(a, frameNo)

	if !isTransition {
		return prov, pixelsA, nil
	}
	b, bOK := r.sampleAt(videoIDs[1], at)
	if !bOK {
		// Fall back to track A but keep the frame marked so the missing
		// secondary source is visible in the manifest.
		prov.GapReason = "secondary track unavailable; primary-only frame"
		return prov, pixelsA, nil
	}
	prov.Transition = true
	prov.SecondaryTrack, prov.SecondarySample, prov.SecondaryOffset = b.TrackID, b.Index, b.Offset
	// Deterministic weight: cross-fade position depends only on frame number.
	weightA := 256 - (frameNo%16)*16 // 240..16 in 16-frame steps, never zero
	if weightA <= 0 {
		weightA = 16
	}
	prov.BlendWeightA = weightA
	pixelsB := rasterFor(b, frameNo)
	return prov, blend(pixelsA, pixelsB, weightA), nil
}

// sampleAt resolves the valid sample covering time at on a video track.
func (r *Renderer) sampleAt(trackID int, at clock.Rational) (model.Sample, bool) {
	tr := r.tracks[trackID]
	if tr == nil || tr.Blocked {
		return model.Sample{}, false
	}
	for _, s := range tr.ValidSamples() {
		end, err := s.End()
		if err != nil {
			continue
		}
		cmpStart, _ := at.Cmp(s.Start)
		cmpEnd, _ := at.Cmp(end)
		if cmpStart >= 0 && cmpEnd < 0 {
			return s, true
		}
	}
	return model.Sample{}, false
}

func (r *Renderer) bestOffset(trackID int) int64 {
	if tr := r.tracks[trackID]; tr != nil {
		return tr.EarliestOffset
	}
	return -1
}

func (r *Renderer) sortedVideoIDs() []int {
	var ids []int
	for id := range r.placed {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j-1] > ids[j]; j-- {
			ids[j-1], ids[j] = ids[j], ids[j-1]
		}
	}
	return ids
}

func (r *Renderer) writeManifest(m *FrameManifest) error {
	path := filepath.Join(r.cfg.OutputDir, "frames.manifest.json")
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func frameName(f int) string {
	const digits = 5
	name := make([]byte, 0, digits+4)
	num := []byte{'0', '0', '0', '0', '0'}
	v := f
	for i := digits - 1; i >= 0; i-- {
		num[i] = byte('0' + v%10)
		v /= 10
	}
	name = append(name, num...)
	name = append(name, '.', 'p', 'p', 'm')
	return string(name)
}

// rasterFor derives deterministic RGB pixels from a sample's source header,
// so identical (track, sample, frame) triples always hash identically.
func rasterFor(s model.Sample, frameNo int) []byte {
	pixels := make([]byte, FrameWidth*FrameHeight*3)
	base := uint32(s.TrackID*7919 + s.Index*104729 + frameNo*31)
	for i := range pixels {
		pixels[i] = byte(base>>((i%3)*8)) ^ byte(i*131+s.Index*17)
	}
	return pixels
}

// gapPixels renders a stable sentinel pattern (dark red bars) for frames
// whose source could not be recovered.
func gapPixels(frameNo int) []byte {
	pixels := make([]byte, FrameWidth*FrameHeight*3)
	for y := 0; y < FrameHeight; y++ {
		for x := 0; x < FrameWidth; x++ {
			i := (y*FrameWidth + x) * 3
			if (x/8+frameNo)%2 == 0 {
				pixels[i] = 128
			}
			pixels[i+2] = 32
		}
	}
	return pixels
}

// blend mixes two rasters: out = (a*wa + b*(256-wa)) / 256, exact integer math.
func blend(a, b []byte, wa int) []byte {
	out := make([]byte, len(a))
	wb := 256 - wa
	for i := range a {
		out[i] = byte((int(a[i])*wa + int(b[i])*wb) >> 8)
	}
	return out
}

func writePPM(path string, pixels []byte) error {
	header := []byte("P6\n64 48\n255\n")
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(header); err != nil {
		return err
	}
	_, err = f.Write(pixels)
	return err
}
