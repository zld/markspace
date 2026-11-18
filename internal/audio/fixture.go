package audio

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// FixtureOptions controls deterministic test-signal synthesis.
type FixtureOptions struct {
	Layout         string  // "mono", "stereo" or "5.1"
	Seconds        float64 // duration, must be > 0
	SampleRate     int     // e.g. 48000
	TargetLoudness float64 // approximate integrated loudness in LUFS
}

// FixtureChannels maps a layout name to channel count and loudness weights
// (weight 0 marks the LFE channel of 5.1).
func FixtureChannels(layout string) (channels int, weights []float64, err error) {
	switch layout {
	case "mono":
		return 1, []float64{1}, nil
	case "stereo":
		return 2, []float64{1, 1}, nil
	case "5.1":
		return 6, []float64{1, 1, 1, 0, 1.41, 1.41}, nil
	}
	return 0, nil, fmt.Errorf("unknown layout %q (want mono, stereo or 5.1)", layout)
}

// WriteFixture synthesizes a deterministic multi-channel 16-bit PCM WAV:
// steady 997 Hz sines per channel (55 Hz on LFE) sized so the K-weighted
// integrated loudness lands near TargetLoudness.
func WriteFixture(path string, o FixtureOptions) error {
	ch, weights, err := FixtureChannels(o.Layout)
	if err != nil {
		return err
	}
	if o.Seconds <= 0 || math.IsNaN(o.Seconds) || math.IsInf(o.Seconds, 0) {
		return fmt.Errorf("seconds must be a positive finite number, got %v", o.Seconds)
	}
	if o.SampleRate < 8000 || o.SampleRate > 384000 {
		return fmt.Errorf("unsupported sample rate %d Hz", o.SampleRate)
	}
	if math.IsNaN(o.TargetLoudness) || math.IsInf(o.TargetLoudness, 0) {
		return fmt.Errorf("invalid target loudness %v", o.TargetLoudness)
	}
	var sumW float64
	for _, w := range weights {
		sumW += w
	}
	// Loudness = -0.691 + 10*log10(sumW * ms); sine mean square = A^2/2.
	ms := math.Pow(10, (o.TargetLoudness+0.691)/10) / sumW
	amp := math.Sqrt(2 * ms)
	if amp > 0.98 {
		return fmt.Errorf("target loudness %.1f LUFS exceeds full scale for layout %s",
			o.TargetLoudness, o.Layout)
	}
	frames := int64(math.Round(o.Seconds * float64(o.SampleRate)))
	dataSize := frames * int64(ch) * 2
	if dataSize > MaxFileSize {
		return fmt.Errorf("fixture would exceed 2 GiB (%d bytes)", dataSize)
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hdr := make([]byte, 0, 44)
	hdr = append(hdr, "RIFF"...)
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(36+dataSize))
	hdr = append(hdr, "WAVE"...)
	hdr = append(hdr, "fmt "...)
	hdr = binary.LittleEndian.AppendUint32(hdr, 16)
	hdr = binary.LittleEndian.AppendUint16(hdr, FormatPCM)
	hdr = binary.LittleEndian.AppendUint16(hdr, uint16(ch))
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(o.SampleRate))
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(o.SampleRate*ch*2))
	hdr = binary.LittleEndian.AppendUint16(hdr, uint16(ch*2))
	hdr = binary.LittleEndian.AppendUint16(hdr, 16)
	hdr = append(hdr, "data"...)
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(dataSize))
	if _, err := f.Write(hdr); err != nil {
		return err
	}

	fs := float64(o.SampleRate)
	buf := make([]byte, ch*2*4096)
	n := 0
	flush := func() error {
		if n == 0 {
			return nil
		}
		_, err := f.Write(buf[:n])
		n = 0
		return err
	}
	for i := int64(0); i < frames; i++ {
		t := float64(i) / fs
		for c := 0; c < ch; c++ {
			freq := 997.0 + 13.0*float64(c)
			if o.Layout == "5.1" && c == 3 {
				freq = 55 // LFE
			}
			v := amp * math.Sin(2*math.Pi*freq*t)
			binary.LittleEndian.PutUint16(buf[n:], uint16(int16(math.Round(v*32767))))
			n += 2
		}
		if n == len(buf) {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}
