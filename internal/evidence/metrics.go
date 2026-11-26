// Package evidence derives objective media statistics from the repaired
// artifacts. Video is decoded offscreen with the standard library JPEG decoder
// (the fixtures are all-intra MJPEG) and audio levels are measured directly
// from PCM samples, so the numbers are deterministic and need no external
// process. The pipeline records the availability of FFmpeg separately; these
// values are always computed here.
package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image/jpeg"
	"math"

	"rtx/internal/model"
)

// CodeEvidence marks a sample that could not be decoded or measured.
const CodeEvidence model.Code = "evidence_failed"

// Sample is a payload plus the track it belongs to, its position on the shared
// media grid and where it came from.
type Sample struct {
	TrackID  uint32
	Fragment string
	Offset   int64
	// Start and End are on the shared media grid.
	Start model.Time
	End   model.Time
	Data  []byte
}

// TrackIDs selects which track is video and which is audio.
type TrackIDs struct {
	Video uint32
	Audio uint32
}

// Metrics is the evidence payload for a run.
type Metrics struct {
	Frames        int     `json:"frames"`
	FrameSHA256   string  `json:"frame_sha256"`
	PixelMean     float64 `json:"pixel_mean"`
	AudioSamples  int     `json:"audio_samples"`
	PeakDBFS      float64 `json:"peak_dbfs"`
	PeakSample    int     `json:"peak_sample"`
	AVSyncMS      float64 `json:"av_sync_ms"`
	VideoStartSec float64 `json:"video_start_sec"`
	VideoEndSec   float64 `json:"video_end_sec"`
	AudioStartSec float64 `json:"audio_start_sec"`
	AudioEndSec   float64 `json:"audio_end_sec"`
}

// Compute measures all samples. Video samples must be decodable JPEG frames and
// audio samples 16-bit big-endian PCM, matching the fixture profile.
func Compute(samples []Sample, tracks TrackIDs) (*Metrics, error) {
	if tracks.Video == tracks.Audio {
		return nil, model.E(model.CodeInvalidInput, "evidence", "video and audio track ids must differ")
	}
	frameHash := sha256.New()
	m := &Metrics{PeakSample: 0}
	var pixelSum float64
	peak := 0
	haveVideo, haveAudio := false, false
	m.VideoStartSec, m.AudioStartSec = math.Inf(1), math.Inf(1)
	m.VideoEndSec, m.AudioEndSec = math.Inf(-1), math.Inf(-1)

	for _, s := range samples {
		if !s.Start.Rate.Valid() {
			return nil, model.E(model.CodeInvalidInput, "evidence", "sample from %s has an invalid rate", s.Fragment)
		}
		switch s.TrackID {
		case tracks.Video:
			img, err := jpeg.Decode(bytes.NewReader(s.Data))
			if err != nil {
				return nil, model.E(CodeEvidence, "decode frame",
					"fragment %s sample at %d is not a decodable JPEG: %v", s.Fragment, s.Offset, err).
					WithPath(s.Fragment).WithByte(s.Offset)
			}
			b := img.Bounds()
			sum, n := 0.0, 0
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					r, g, bl, _ := img.At(x, y).RGBA()
					sum += 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
					n++
				}
			}
			if n == 0 {
				return nil, model.E(CodeEvidence, "decode frame", "empty frame at %d", s.Offset).WithPath(s.Fragment).WithByte(s.Offset)
			}
			m.Frames++
			pixelSum += sum / float64(n)
			frameHash.Write(s.Data)
			if s.Start.Seconds() < m.VideoStartSec {
				m.VideoStartSec = s.Start.Seconds()
			}
			if s.End.Seconds() > m.VideoEndSec {
				m.VideoEndSec = s.End.Seconds()
			}
			haveVideo = true
		case tracks.Audio:
			if len(s.Data)%2 != 0 {
				return nil, model.E(CodeEvidence, "measure audio",
					"fragment %s sample at %d has odd PCM length %d", s.Fragment, s.Offset, len(s.Data)).
					WithPath(s.Fragment).WithByte(s.Offset)
			}
			for i := 0; i+1 < len(s.Data); i += 2 {
				v := int(int16(binary.BigEndian.Uint16(s.Data[i : i+2])))
				if v < 0 {
					v = -v
				}
				if v > peak {
					peak = v
				}
			}
			m.AudioSamples += len(s.Data) / 2
			if s.Start.Seconds() < m.AudioStartSec {
				m.AudioStartSec = s.Start.Seconds()
			}
			if s.End.Seconds() > m.AudioEndSec {
				m.AudioEndSec = s.End.Seconds()
			}
			haveAudio = true
		}
	}
	if !haveVideo || !haveAudio {
		return nil, model.E(CodeEvidence, "compute",
			"need both video and audio samples (video=%v audio=%v)", haveVideo, haveAudio)
	}
	if peak > 0 {
		m.PeakSample = peak
		m.PeakDBFS = 20 * math.Log10(float64(peak)/32768.0)
	} else {
		m.PeakDBFS = -math.Inf(1)
	}
	m.PixelMean = pixelSum / float64(m.Frames)
	m.FrameSHA256 = hex.EncodeToString(frameHash.Sum(nil))
	ds := math.Abs(m.VideoStartSec - m.AudioStartSec)
	de := math.Abs(m.VideoEndSec - m.AudioEndSec)
	if ds > de {
		m.AVSyncMS = ds * 1000
	} else {
		m.AVSyncMS = de * 1000
	}
	return m, nil
}

// Validate enforces the acceptance tolerances for fixture runs.
func (m *Metrics) Validate(wantPixelCenter, pixelTol, wantPeak, peakTol, syncMS float64) error {
	if math.Abs(m.PixelMean-wantPixelCenter) > pixelTol {
		return model.E(CodeEvidence, "validate evidence",
			"pixel_mean %.3f is outside %.1f±%.1f", m.PixelMean, wantPixelCenter, pixelTol)
	}
	if math.Abs(m.PeakDBFS-wantPeak) > peakTol {
		return model.E(CodeEvidence, "validate evidence",
			"peak_dbfs %.3f is outside %.1f±%.1f", m.PeakDBFS, wantPeak, peakTol)
	}
	if m.AVSyncMS > syncMS {
		return model.E(CodeEvidence, "validate evidence",
			"av_sync_ms %.3f exceeds %.1f", m.AVSyncMS, syncMS)
	}
	return nil
}

// SummaryLine renders the stable one-line evidence record.
func (m *Metrics) SummaryLine() string {
	return fmt.Sprintf("frames=%d pixel_mean=%.2f peak_dbfs=%.2f av_sync_ms=%.2f frame_sha256=%s",
		m.Frames, m.PixelMean, m.PeakDBFS, m.AVSyncMS, m.FrameSHA256)
}
