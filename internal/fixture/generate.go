package fixture

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"loudscan/internal/ebu"
)

// spec describes one generated WAV.
type spec struct {
	Name     string
	Channels []string
	Seconds  float64
	Rate     int
	// TargetLUFS is the integrated loudness the tone should measure. The
	// amplitude is solved from the K-weighting response at the tone frequency
	// and the layout weights, so fixtures are calibrated rather than guessed.
	TargetLUFS float64
	// ToneHz is the test tone frequency for the measured channels.
	ToneHz float64
	// LFEHz and LFEGain control the LFE channel, which is not measured.
	LFEHz   float64
	LFEGain float64
	// Hot writes a sine well above full scale to exercise the peak limit.
	Hot     bool
	Silence bool
	Intent  string
}

// Set is the generated fixture collection.
type Set struct {
	Dir   string
	Seed  int64
	Files []File
}

// File describes one generated WAV.
type File struct {
	Path     string
	Channels int
	Rate     int
	Layout   string
	Intent   string
}

var specs = []spec{
	{
		Name:     "silence_stereo.wav",
		Channels: []string{"FL", "FR"},
		Rate:     48000,
		Seconds:  4,
		Silence:  true,
		Intent:   "digital silence must report no_measurement",
	},
	{
		Name:       "stereo_sine_1khz.wav",
		Channels:   []string{"FL", "FR"},
		Rate:       48000,
		Seconds:    8,
		TargetLUFS: -23,
		ToneHz:     1000,
		Intent:     "stereo 1 kHz sine calibrated to -23 LUFS",
	},
	{
		Name:       "surround_5_1.wav",
		Channels:   []string{"FL", "FR", "FC", "LFE", "BL", "BR"},
		Rate:       48000,
		Seconds:    8,
		TargetLUFS: -23,
		ToneHz:     1000,
		LFEHz:      60,
		LFEGain:    3,
		Intent:     "5.1 layout; loud LFE is excluded from loudness",
	},
	{
		Name:     "hot_stereo_peak.wav",
		Channels: []string{"FL", "FR"},
		Rate:     48000,
		Seconds:  4,
		ToneHz:   997,
		Hot:      true,
		Intent:   "above-full-scale peak exceeds the -1 dBTP limit",
	},
	{
		Name:       "quad_side_44k.wav",
		Channels:   []string{"FL", "FR", "SL", "SR"},
		Rate:       44100,
		Seconds:    6,
		TargetLUFS: -23,
		ToneHz:     1000,
		Intent:     "44.1 kHz quad(side) layout exercises surround weighting",
	},
	{
		Name:       "mono_center.wav",
		Channels:   []string{"FC"},
		Rate:       48000,
		Seconds:    5,
		TargetLUFS: -26,
		ToneHz:     440,
		Intent:     "mono fixture, 3 dB quieter than its stereo twin",
	},
}

// Generate writes the deterministic fixture set into dir. Files are
// overwritten. The tones are analytic, so the sample data does not depend on
// seed; the seed is recorded in the Set so a demo log can name the exact
// command that produced a fixture directory.
func Generate(dir string, seed int64) (Set, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Set{}, fmt.Errorf("create fixture dir: %w", err)
	}
	set := Set{Dir: dir, Seed: seed}
	for _, s := range specs {
		path := filepath.Join(dir, s.Name)
		if err := writeWAV(path, s); err != nil {
			return Set{}, err
		}
		set.Files = append(set.Files, File{
			Path:     path,
			Channels: len(s.Channels),
			Rate:     s.Rate,
			Layout:   strings.Join(s.Channels, "-"),
			Intent:   s.Intent,
		})
	}
	return set, nil
}

// toneAmplitude solves for the sine amplitude that measures targetLUFS on the
// measured channels of a layout.
func toneAmplitude(s spec) (float64, error) {
	gain, err := ebu.KWeightingGain(s.Rate, s.ToneHz)
	if err != nil {
		return 0, err
	}
	layout, err := ebu.NewLayout(s.Channels)
	if err != nil {
		return 0, err
	}
	var weightSum float64
	for i := 0; i < layout.Len(); i++ {
		weightSum += layout.Weight(i)
	}
	if weightSum == 0 {
		return 0, fmt.Errorf("%s: layout has no measured channels", s.Name)
	}
	// target = -0.691 + 10*log10(weightSum * A^2/2 * gain^2)
	energy := math.Pow(10, (s.TargetLUFS+0.691)/10) / (weightSum * gain * gain)
	return math.Sqrt(2 * energy), nil
}

// writeWAV writes 32-bit IEEE float WAVE_FORMAT_EXTENSIBLE, so the channel
// mask survives and FFmpeg reports the intended layout. Float samples may
// exceed 1.0, which is exactly what the hot fixture needs. The tone starts at
// phase pi/2 so it begins at its amplitude instead of at zero: the loudness
// blocks then contain full-level material from the first sample. The phase
// offset is constant, so every channel keeps the same K-weighted RMS and the
// layout weighting would be unaffected even if it were not.
func writeWAV(path string, s spec) error {
	ch := len(s.Channels)
	frames := int(math.Round(s.Seconds * float64(s.Rate)))
	dataBytes := frames * ch * 4

	amp := 0.0
	if !s.Silence {
		if s.Hot {
			amp = 1.6
		} else {
			var err error
			amp, err = toneAmplitude(s)
			if err != nil {
				return fmt.Errorf("%s: %w", s.Name, err)
			}
		}
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()

	header := make([]byte, 0, 80)
	header = append(header, "RIFF"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(60+dataBytes))
	header = append(header, "WAVE"...)
	header = append(header, "fmt "...)
	header = binary.LittleEndian.AppendUint32(header, 40)
	header = binary.LittleEndian.AppendUint16(header, 0xFFFE) // WAVE_FORMAT_EXTENSIBLE
	header = binary.LittleEndian.AppendUint16(header, uint16(ch))
	header = binary.LittleEndian.AppendUint32(header, uint32(s.Rate))
	header = binary.LittleEndian.AppendUint32(header, uint32(s.Rate*ch*4))
	header = binary.LittleEndian.AppendUint16(header, uint16(ch*4))
	header = binary.LittleEndian.AppendUint16(header, 32)
	header = binary.LittleEndian.AppendUint16(header, 22) // cbSize
	header = binary.LittleEndian.AppendUint16(header, 32) // valid bits per sample
	header = binary.LittleEndian.AppendUint32(header, channelMask(s.Channels))
	// Subformat GUID for IEEE float PCM.
	header = append(header, 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00,
		0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71)
	header = append(header, "data"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(dataBytes))
	if _, err := f.Write(header); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	const blockFrames = 4096
	buf := make([]byte, 0, blockFrames*ch*4)
	for start := 0; start < frames; start += blockFrames {
		n := blockFrames
		if start+n > frames {
			n = frames - start
		}
		buf = buf[:0]
		for i := 0; i < n; i++ {
			t := float64(start+i) / float64(s.Rate)
			for c := 0; c < ch; c++ {
				v := 0.0
				if !s.Silence {
					if s.Channels[c] == "LFE" {
						v = amp * s.LFEGain * math.Cos(2*math.Pi*s.LFEHz*t)
					} else {
						v = amp * math.Cos(2*math.Pi*s.ToneHz*t)
					}
				}
				buf = binary.LittleEndian.AppendUint32(buf, math.Float32bits(float32(v)))
			}
		}
		if _, err := f.Write(buf); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return f.Sync()
}

// channelMask returns the WAVE speaker mask for a label list.
func channelMask(labels []string) uint32 {
	bits := map[string]uint32{
		"FL": 0x1, "FR": 0x2, "FC": 0x4, "LFE": 0x8,
		"BL": 0x10, "BR": 0x20, "SL": 0x200, "SR": 0x400,
	}
	var mask uint32
	for _, l := range labels {
		mask |= bits[l]
	}
	return mask
}
