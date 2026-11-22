// Package render produces off-screen, evidence-grade frame sequences without
// any graphical dependency. Each frame is a fixed-size synthetic raster whose
// pixels are a pure function of the manifest timeline position and track
// state, so identical inputs always yield identical pixel digests and
// SHA-256 hashes across repeated runs.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"github.com/medialab/medialab/internal/clock"
)

// FrameWidth and FrameHeight fix the evidence raster at 64x48 grayscale.
const (
	FrameWidth  = 64
	FrameHeight = 48
)

// crcRasterTable is the IEEE table used for pixel digests.
var crcRasterTable = crc32.MakeTable(crc32.IEEE)

func crcRaster(raster []byte) uint32 {
	return crc32.Checksum(raster, crcRasterTable)
}

// Frame is one rendered, hash-annotated evidence frame.
type Frame struct {
	Index       int    `json:"index"`
	TrackID     string `json:"track_id"`
	ManifestMs  int64  `json:"manifest_ms"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	PixelDigest uint32 `json:"pixel_digest"` // CRC-32 of the raster bytes
	SHA256      string `json:"sha256"`       // SHA-256 of the raster bytes
	WaitingKF   bool   `json:"waiting_keyframe"`
	Quality     float64 `json:"quality"`
}

// Config parameterizes the renderer; output goes through a WriterOpener so
// render-write failures can be injected in tests.
type Config struct {
	OutDir string
	Width  int
	Height int
}

// WriterOpener abstracts file creation for deterministic failure injection.
type WriterOpener func(path string) (io.WriteCloser, error)

// FSWriter opens real files under the configured output directory.
func FSWriter(path string) (io.WriteCloser, error) {
	return os.Create(path)
}

// Renderer draws frames and writes one binary raster plus a JSON sidecar per
// frame. It holds no frame history, so memory is independent of material
// length.
type Renderer struct {
	cfg    Config
	open   WriterOpener
	count  int
}

// NewRenderer builds a renderer with default frame dimensions when unset.
func NewRenderer(cfg Config, opener WriterOpener) *Renderer {
	if cfg.Width <= 0 {
		cfg.Width = FrameWidth
	}
	if cfg.Height <= 0 {
		cfg.Height = FrameHeight
	}
	if opener == nil {
		opener = FSWriter
	}
	return &Renderer{cfg: cfg, open: opener}
}

// Draw creates the raster for one timeline position. Pixel values derive only
// from the tick, index and waiting flag, guaranteeing repeatability.
func (r *Renderer) Draw(trackID string, atTick int64, index int, waitingKF bool) Frame {
	raster := r.buildRaster(clock.ToMillis(atTick), index, waitingKF)
	sum := sha256.Sum256(raster)
	quality := 1.0
	if waitingKF {
		quality = 0.85
	}
	return Frame{
		Index:       index,
		TrackID:     trackID,
		ManifestMs:  clock.ToMillis(atTick),
		Width:       r.cfg.Width,
		Height:      r.cfg.Height,
		PixelDigest: crcRaster(raster),
		SHA256:      hex.EncodeToString(sum[:]),
		WaitingKF:   waitingKF,
		Quality:     quality,
	}
}

// buildRaster is the single source of pixel values: a pure function of the
// manifest millisecond, frame index and waiting flag.
func (r *Renderer) buildRaster(manifestMs int64, index int, waitingKF bool) []byte {
	w, h := r.cfg.Width, r.cfg.Height
	raster := make([]byte, w*h)
	base := byte(uint64(manifestMs) % 251)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := int(base) ^ (x*7 + y*13) ^ (index * 31)
			if waitingKF {
				v = v >> 1 // visibly degraded band while awaiting a keyframe
			}
			raster[y*w+x] = byte(v)
		}
	}
	return raster
}

// Write persists one frame as <dir>/frame_%06d.bin plus a .json sidecar.
// Any opener/writer failure is wrapped as KindRenderFailure with location.
func (r *Renderer) Write(f Frame, raster []byte) error {
	stem := frameStem(f.Index)
	binPath := filepath.Join(r.cfg.OutDir, stem+".bin")
	wc, err := r.open(binPath)
	if err != nil {
		return clock.NewError(clock.KindRenderFailure, "render", f.TrackID,
			f.ManifestMs, "open raster: "+err.Error())
	}
	if _, err := wc.Write(raster); err != nil {
		_ = wc.Close()
		return clock.NewError(clock.KindRenderFailure, "render", f.TrackID,
			f.ManifestMs, "write raster: "+err.Error())
	}
	if err := wc.Close(); err != nil {
		return clock.NewError(clock.KindRenderFailure, "render", f.TrackID,
			f.ManifestMs, "close raster: "+err.Error())
	}
	jsonPath := filepath.Join(r.cfg.OutDir, stem+".json")
	meta, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return clock.NewError(clock.KindRenderFailure, "render", f.TrackID,
			f.ManifestMs, err.Error())
	}
	if err := os.WriteFile(jsonPath, meta, 0o644); err != nil {
		return clock.NewError(clock.KindRenderFailure, "render", f.TrackID,
			f.ManifestMs, "write sidecar: "+err.Error())
	}
	r.count++
	return nil
}

// Count returns how many frames have been written.
func (r *Renderer) Count() int { return r.count }

// Raster reproduces the exact bytes that Draw hashed, so callers can persist
// and independently verify them.
func (r *Renderer) Raster(f Frame) []byte {
	return r.buildRaster(f.ManifestMs, f.Index, f.WaitingKF)
}

func frameStem(index int) string {
	const digits = "0123456789"
	out := make([]byte, 6)
	n := index
	for i := 5; i >= 0; i-- {
		out[i] = digits[n%10]
		n /= 10
	}
	return "frame_" + string(out)
}
