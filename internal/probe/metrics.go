package probe

import (
	"encoding/binary"
	"errors"
	"image"
	"math"
	"os"

	"mediarepair/internal/media"
)

type PixelStats struct {
	Pixels int    `json:"pixels"`
	SumR   uint64 `json:"sum_r"`
	SumG   uint64 `json:"sum_g"`
	SumB   uint64 `json:"sum_b"`
}

func ImagePixelStats(img image.Image) PixelStats {
	b := img.Bounds()
	var st PixelStats
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			// RGBA returns pre-multiplied 16-bit values. Opaque fixtures are
			// fully opaque; normalize to 8-bit color values.
			st.SumR += uint64(r >> 8)
			st.SumG += uint64(g >> 8)
			st.SumB += uint64(bl >> 8)
			st.Pixels++
		}
	}
	return st
}

func (p *PixelStats) Add(other PixelStats) {
	p.Pixels += other.Pixels
	p.SumR += other.SumR
	p.SumG += other.SumG
	p.SumB += other.SumB
}

func (p PixelStats) Means() (float64, float64, float64) {
	if p.Pixels == 0 {
		return 0, 0, 0
	}
	n := float64(p.Pixels)
	return float64(p.SumR) / n, float64(p.SumG) / n, float64(p.SumB) / n
}

// FormatMean prints compact values such as 93,95.5,108.5.
func FormatMean(v float64) string {
	r := math.Round(v*10) / 10
	if r == math.Trunc(r) {
		return formatInt(int64(r))
	}
	return formatFloatTenth(r)
}

func FormatMeanRGB(r, g, b float64) string {
	return FormatMean(r) + "," + FormatMean(g) + "," + FormatMean(b)
}

type WAVStats struct {
	SampleRate int       `json:"sample_rate"`
	Channels   int       `json:"channels"`
	Frames     int       `json:"frames"`
	Peak       float64   `json:"peak"`
	PeakDBFS   float64   `json:"peak_dbfs"`
	Samples    []float64 `json:"-"`
}

func ReadWAV(path string) (*WAVStats, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, media.E(media.CodeWrite, "wav.read", err).WithFile(path)
	}
	st, err := ParseWAV(b)
	if err != nil {
		return nil, media.AsError(err).WithFile(path)
	}
	return st, nil
}

func ParseWAV(b []byte) (*WAVStats, error) {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, media.E(media.CodeFormat, "wav.header", errors.New("not a RIFF/WAVE file")).WithOffset(0)
	}
	channels := int(binary.LittleEndian.Uint16(b[22:24]))
	sampleRate := int(binary.LittleEndian.Uint32(b[24:28]))
	bits := int(binary.LittleEndian.Uint16(b[34:36]))
	if format := binary.LittleEndian.Uint16(b[20:22]); format != 1 {
		return nil, media.E(media.CodeFormat, "wav.format", errors.New("only PCM WAV is supported")).WithOffset(20)
	}
	if channels <= 0 || sampleRate <= 0 || bits != 16 {
		return nil, media.E(media.CodeFormat, "wav.params", errors.New("supported WAV must be PCM16 with positive rate/channels")).WithOffset(22)
	}

	data, err := findChunk(b, "data")
	if err != nil {
		return nil, err
	}
	if len(data)%2 != 0 {
		return nil, media.E(media.CodeFormat, "wav.data", errors.New("truncated PCM16 sample")).WithOffset(int64(len(b)))
	}
	st := &WAVStats{SampleRate: sampleRate, Channels: channels}
	norm := float64(1 << 15)
	for off := 0; off+2 <= len(data); off += 2 {
		v := int16(binary.LittleEndian.Uint16(data[off : off+2]))
		f := float64(v) / norm
		st.Samples = append(st.Samples, f)
		if a := math.Abs(f); a > st.Peak {
			st.Peak = a
		}
	}
	if channels > 0 {
		st.Frames = len(st.Samples) / channels
	}
	if st.Peak > 0 {
		st.PeakDBFS = 20 * math.Log10(st.Peak)
	} else {
		st.PeakDBFS = math.Inf(-1)
	}
	return st, nil
}

func findChunk(b []byte, want string) ([]byte, error) {
	pos := 12
	for pos+8 <= len(b) {
		id := string(b[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
		start := pos + 8
		end := start + size
		if end > len(b) {
			return nil, media.E(media.CodeFormat, "wav.chunk", errors.New("chunk extends past file")).WithOffset(int64(len(b)))
		}
		if id == want {
			return b[start:end], nil
		}
		pos = end + (size & 1)
	}
	return nil, media.E(media.CodeFormat, "wav.data_chunk", errors.New("missing data chunk")).WithOffset(int64(len(b)))
}

// DetectOnsets finds loud impulse starts separated by minDistance samples.
func DetectOnsets(samples []float64, threshold float64, minDistance int) []int {
	var onsets []int
	last := -minDistance - 1
	for i, v := range samples {
		if math.Abs(v) >= threshold && i-last >= minDistance {
			onsets = append(onsets, i)
			last = i
		}
	}
	return onsets
}

// AlignmentErrorUS compares corresponding visual and audio event times.
// visualTimesUS are integer rounded; audio positions are converted exactly.
func AlignmentErrorUS(visualTimesUS []int64, audioSamplePositions []int, sampleRate int) (maxAbsUS float64, errs []float64) {
	n := len(visualTimesUS)
	if len(audioSamplePositions) < n {
		n = len(audioSamplePositions)
	}
	for i := 0; i < n; i++ {
		audioUS := float64(audioSamplePositions[i]) * 1_000_000 / float64(sampleRate)
		diff := math.Abs(float64(visualTimesUS[i]) - audioUS)
		errs = append(errs, diff)
		if diff > maxAbsUS {
			maxAbsUS = diff
		}
	}
	return maxAbsUS, errs
}

func formatInt(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [24]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func formatFloatTenth(v float64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	tenths := int64(math.Round(v * 10))
	s := formatInt(tenths/10) + "." + string(rune('0'+tenths%10))
	if neg {
		return "-" + s
	}
	return s
}
