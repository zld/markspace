// Package validate independently decodes delivered media and checks metrics.
package validate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image/jpeg"
	"io"
	"math"
	"os"

	"replayctl/internal/fault"
	mp4scan "replayctl/internal/mp4"
)

// Metrics are independently measured from a delivered MP4.
type Metrics struct {
	Frames                    int     `json:"frames"`
	Pixels                    int64   `json:"pixels"`
	AudioSamples              int     `json:"audio_samples"`
	Peak                      int16   `json:"peak"`
	PeakDBFS                  float64 `json:"peak_dbfs"`
	VideoAlignErrorMS         int64   `json:"video_align_error_ms"`
	AudioBoundaryErrorSamples int64   `json:"audio_boundary_error_samples"`
	SHA256                    string  `json:"sha256"`
}

// Expectations are the acceptance bounds and exact counts for the fixture.
type Expectations struct {
	Frames                  int
	Pixels                  int64
	AudioSamples            int
	VideoAlignMaxMS         int64
	AudioBoundaryMaxSamples int64
}

// HashFile returns the lowercase 64-character SHA-256 hex digest.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fault.New(fault.IO, fault.StageValidate, path, -1, -1, "open hash input: %v", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fault.New(fault.IO, fault.StageValidate, path, -1, -1, "hash: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// MeasureFile parses and decodes the delivered file.
func MeasureFile(path string) (*Metrics, error) {
	mov, err := mp4scan.ScanFile(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.New(fault.IO, fault.StageValidate, path, -1, -1, "open media: %v", err)
	}
	defer f.Close()

	m := &Metrics{}
	if tr := mov.Track("video"); tr != nil {
		for i := range tr.Samples {
			data, err := mov.ReadSampleAt(f, "video", i)
			if err != nil {
				return nil, err
			}
			img, err := jpeg.Decode(bytes.NewReader(data))
			if err != nil {
				smp := tr.Samples[i]
				return nil, fault.New(fault.Validation, fault.StageValidate, path, smp.Offset, smp.DTS, "decode output JPEG %d: %v", i, err)
			}
			b := img.Bounds()
			m.Frames++
			m.Pixels += int64(b.Dx() * b.Dy())
		}
	}
	if tr := mov.Track("audio"); tr != nil && len(tr.Samples) > 0 {
		pcm, err := readPCM(mov, f, path)
		if err != nil {
			return nil, err
		}
		m.AudioSamples = len(pcm)
		var peak int32
		for _, v := range pcm {
			a := int32(v)
			if a < 0 {
				a = -a
			}
			if a > peak {
				peak = a
			}
		}
		m.Peak = int16(peak)
		if peak > 0 {
			m.PeakDBFS = 20 * math.Log10(float64(peak)/32768.0)
		}
	}
	m.SHA256, err = HashFile(path)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// Check verifies exact counts and alignment limits.
func (m *Metrics) Check(e Expectations) error {
	if m.Frames != e.Frames {
		return fault.New(fault.Validation, fault.StageValidate, "", -1, -1, "frames=%d want %d", m.Frames, e.Frames)
	}
	if m.Pixels != e.Pixels {
		return fault.New(fault.Validation, fault.StageValidate, "", -1, -1, "pixels=%d want %d", m.Pixels, e.Pixels)
	}
	if m.AudioSamples != e.AudioSamples {
		return fault.New(fault.Validation, fault.StageValidate, "", -1, -1, "audio_samples=%d want %d", m.AudioSamples, e.AudioSamples)
	}
	if m.VideoAlignErrorMS > e.VideoAlignMaxMS {
		return fault.New(fault.Validation, fault.StageValidate, "", -1, -1, "video alignment %dms > %dms", m.VideoAlignErrorMS, e.VideoAlignMaxMS)
	}
	if m.AudioBoundaryErrorSamples > e.AudioBoundaryMaxSamples {
		return fault.New(fault.Validation, fault.StageValidate, "", -1, -1, "audio boundary error %d > %d", m.AudioBoundaryErrorSamples, e.AudioBoundaryMaxSamples)
	}
	return nil
}

// NearPeak reports whether measured peak is within tolerance of -12.04 dBFS.
func (m *Metrics) NearPeak() bool {
	return math.Abs(m.PeakDBFS+12.04) <= 0.01
}
