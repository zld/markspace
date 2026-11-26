package testfixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"

	"mediarepair/internal/fsx"
	"mediarepair/internal/media"
	"mediarepair/internal/source"
)

const (
	FrameCount     = 12
	FrameSize      = 16
	SampleRate48k  = 48000
	FramesPerBlock = 4
)

// PixelRGB defines the deterministic acceptance image. Its aggregate mean is
// exactly 93,95.5,108.5 for an even total pixel count.
func PixelRGB(globalPixel int) (uint8, uint8, uint8) {
	parity := globalPixel & 1
	return 93, uint8(95 + parity), uint8(108 + parity)
}

func BuildFramePNG(frame, width, height int) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	n := width * height
	parity := make([]byte, n)
	positions := make([]int, n)
	for i := range positions {
		positions[i] = i
	}
	// Exactly half of the pixels use the +1 values, preserving the required
	// aggregate mean while giving each frame a different content hash.
	rng := rand.New(rand.NewSource(int64(frame + 1)))
	rng.Shuffle(n, func(i, j int) { positions[i], positions[j] = positions[j], positions[i] })
	for i := 0; i < n/2; i++ {
		parity[positions[i]] = 1
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			p := int(parity[y*width+x])
			r, g, b := PixelRGB(p)
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// BuildBeatWAV builds mono PCM16. Beat impulses land exactly on four-frame
// boundaries at 24 fps, giving zero rational alignment error.
func BuildBeatWAV(sampleRate, samples int) []byte {
	pcm := make([]int16, samples)
	beatInterval := sampleRate / 6 // four 24fps frames = 1/6 second
	for n := 0; n < samples; n++ {
		phase := n % beatInterval
		env := math.Exp(-float64(phase) / 90.0)
		v := 0.5 * env
		pcm[n] = int16(math.Round(v * 32768))
	}

	dataBytes := len(pcm) * 2
	out := make([]byte, 44+dataBytes)
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(36+dataBytes))
	copy(out[8:12], "WAVE")
	copy(out[12:16], "fmt ")
	binary.LittleEndian.PutUint32(out[16:20], 16)
	binary.LittleEndian.PutUint16(out[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:24], 1) // mono
	binary.LittleEndian.PutUint32(out[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(out[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(out[32:34], 2)
	binary.LittleEndian.PutUint16(out[34:36], 16)
	copy(out[36:40], "data")
	binary.LittleEndian.PutUint32(out[40:44], uint32(dataBytes))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(out[44+i*2:46+i*2], uint16(v))
	}
	return out
}

type StandardFixture struct {
	Root         string
	Manifest     *source.Manifest
	FrameBytes   [][]byte
	AudioBytes   []byte
	MP4Bytes     []byte
	ExpectedHash string
}

func CreateStandard(root string) (*StandardFixture, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, media.E(media.CodeWrite, "fixture.mkdir", err).WithFile(root)
	}
	fx := &StandardFixture{Root: root, FrameBytes: make([][]byte, FrameCount)}
	m := &source.Manifest{
		Version:   source.ManifestVersion,
		Track:     1,
		FrameRate: media.MustRational(24, 1),
		Timebase:  media.MustRational(1, SampleRate48k),
	}

	mp4Samples := make([][]byte, 0, FrameCount)
	for i := 0; i < FrameCount; i++ {
		b, err := BuildFramePNG(i, FrameSize, FrameSize)
		if err != nil {
			return nil, err
		}
		fx.FrameBytes[i] = b
		mp4Samples = append(mp4Samples, b)
		seq := i + 1
		rel := filepath.ToSlash(filepath.Join("frames", "frame_"+fourDigits(seq)+".png"))
		full, err := fsx.SafeJoin(root, rel)
		if err != nil {
			return nil, err
		}
		if err := fsx.AtomicWriteFile(full, bytes.NewReader(b), 0o644); err != nil {
			return nil, err
		}
		raw := media.ExpectedTicks(i, m.FrameRate, m.Timebase)
		if seq == 4 {
			raw = 5000 // one tick-time defect
		}
		if seq == 9 {
			raw = 17000 // second defect
		}
		ts := uint32(raw)
		m.Frames = append(m.Frames, source.FrameRecord{
			Track:        1,
			Sequence:     seq,
			RawTicks:     raw,
			Timebase:     m.Timebase,
			Width:        FrameSize,
			Height:       FrameSize,
			Format:       "png",
			Color:        source.ColorMetadata{Primaries: "bt709", Transfer: "iec61966-2-1", Matrix: "bt709", Range: "full"},
			Path:         rel,
			SHA256:       SHA256Hex(b),
			RTPTimestamp: &ts,
			RTPMarker:    seq%FramesPerBlock == 0,
		})
	}

	fx.AudioBytes = BuildBeatWAV(SampleRate48k, SampleRate48k/2)
	audioRel := filepath.ToSlash(filepath.Join("audio", "beat-48k.wav"))
	audioPath, err := fsx.SafeJoin(root, audioRel)
	if err != nil {
		return nil, err
	}
	if err := fsx.AtomicWriteFile(audioPath, bytes.NewReader(fx.AudioBytes), 0o644); err != nil {
		return nil, err
	}
	m.Audio = &source.AudioReference{Path: audioRel, SHA256: SHA256Hex(fx.AudioBytes), SampleRate: SampleRate48k, Channels: 1}

	fx.MP4Bytes, err = BuildMinimalMP4(mp4Samples, SampleRate48k, 2000)
	if err != nil {
		return nil, err
	}
	mp4Rel := filepath.ToSlash(filepath.Join("packaging", "frames.mp4"))
	mp4Path, err := fsx.SafeJoin(root, mp4Rel)
	if err != nil {
		return nil, err
	}
	if err := fsx.AtomicWriteFile(mp4Path, bytes.NewReader(fx.MP4Bytes), 0o644); err != nil {
		return nil, err
	}
	m.MP4 = &source.MP4Reference{Path: mp4Rel, SHA256: SHA256Hex(fx.MP4Bytes)}

	manifestPath := filepath.Join(root, "manifest.json")
	if err := source.SaveManifest(manifestPath, m); err != nil {
		return nil, err
	}
	fx.Manifest = m
	return fx, nil
}

func fourDigits(v int) string {
	if v < 10 {
		return "000" + string(rune('0'+v))
	}
	if v < 100 {
		return "00" + itoa(v)
	}
	if v < 1000 {
		return "0" + itoa(v)
	}
	return itoa(v)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
