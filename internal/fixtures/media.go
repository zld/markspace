// Package fixtures synthesizes the example inputs used by `rtx fixtures`.
// All media is generated from Go code; nothing is downloaded or copied. The
// output is deterministic so repeated generation yields identical digests.
package fixtures

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
)

// Video geometry and timing shared by every fixture.
const (
	Width          = 320
	Height         = 180
	FPS            = 25
	VideoTimescale = 90000
	AudioTimescale = 48000
	SampleRate     = 48000
	Channels       = 1

	// VideoTicksPerFrame is one frame at 25 fps on the 90 kHz video timescale.
	VideoTicksPerFrame = VideoTimescale / FPS // 3600
	// AudioTicksPerFrame is one video-frame worth of PCM samples (1920) on the
	// 48 kHz audio timescale.
	AudioTicksPerFrame = AudioTimescale / FPS // 1920
	// MediaRate is the unified grid: lcm(90000,48000) = 720000.
	MediaRateNum = 720000
	MediaRateDen = 1

	// PixelY is the luma every frame is generated at, so decoded evidence can
	// assert pixel_mean=96±2.
	PixelY = 96
	// PeakAmplitude is 0.5 full scale, i.e. -6.02 dBFS.
	PeakAmplitude = 16384
	ToneHz        = 440
)

// JPEGFrame encodes one deterministic frame. A single +1 luma pixel keeps every
// frame a distinct byte sequence (so frame hashes are meaningful) while the
// average luma stays inside the 96±2 acceptance band.
func JPEGFrame(index int) []byte {
	img := image.NewGray(image.Rect(0, 0, Width, Height))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.Gray{Y: PixelY}}, image.Point{}, draw.Src)
	img.Pix[(index%Height)*Width] = PixelY + 1
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		panic(fmt.Sprintf("fixtures: encode frame: %v", err))
	}
	return b.Bytes()
}

func clamp8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// PCMFrame returns one video-frame worth of 16-bit big-endian mono PCM at
// -6.02 dBFS. The tone is phase-continuous across frames when called with the
// absolute sample index offset.
func PCMFrame(startSample int, samples int) []byte {
	out := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		t := float64(startSample+i) / float64(SampleRate)
		v := int16(math.Round(PeakAmplitude * math.Sin(2*math.Pi*ToneHz*t)))
		out[i*2] = byte(uint16(v) >> 8)
		out[i*2+1] = byte(uint16(v))
	}
	return out
}
