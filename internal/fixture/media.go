// Package fixture generates deterministic local MJPEG/PCM and RTP test media.
package fixture

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"

	mp4scan "replayctl/internal/mp4"
)

const (
	Width      = 16
	Height     = 16
	FPS        = 25
	VideoDelta = 3600
	VideoRate  = 90_000
	AudioRate  = 48_000
	DurationMS = 2_000
	FrameCount = DurationMS / 1000 * FPS // 50
	AudioCount = DurationMS / 1000 * AudioRate
	EventID    = "kill-1000ms"
	EventKind  = "kill"
	EventMS    = 1000
)

// GeneratedMedia contains mux inputs and deterministic source samples.
type GeneratedMedia struct {
	Video       mp4scan.TrackIn
	Audio       mp4scan.TrackIn
	PCM         []int16
	FrameImages []*image.RGBA
}

// GenerateMedia builds 50 JPEG frames and two seconds of 48 kHz mono PCM.
func GenerateMedia() (*GeneratedMedia, error) {
	g := &GeneratedMedia{PCM: make([]int16, AudioCount), FrameImages: make([]*image.RGBA, FrameCount)}
	var videoData bytes.Buffer
	sizes := make([]uint32, 0, FrameCount)
	sync := make([]uint32, 0, FrameCount)
	for i := 0; i < FrameCount; i++ {
		img := frameImage(i)
		g.FrameImages[i] = img
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			return nil, err
		}
		sizes = append(sizes, uint32(buf.Len()))
		videoData.Write(buf.Bytes())
		sync = append(sync, uint32(i+1))
	}
	g.Video = mp4scan.TrackIn{
		Kind: "video", Codec: "jpeg ", Timescale: VideoRate,
		Width: Width, Height: Height, Data: videoData.Bytes(),
		SampleSizes: sizes, SampleCount: FrameCount, Delta: VideoDelta, SyncSamples: sync,
	}

	audioBytes := make([]byte, AudioCount*2)
	for n := 0; n < AudioCount; n++ {
		// 1 kHz tone. n=12 is exactly a sine peak and amplitude 8192 is -12.04 dBFS.
		v := int16(math.Round(8192 * math.Sin(2*math.Pi*1000*float64(n)/AudioRate)))
		g.PCM[n] = v
		audioBytes[2*n] = byte(v)
		audioBytes[2*n+1] = byte(uint16(v) >> 8)
	}
	g.Audio = mp4scan.TrackIn{
		Kind: "audio", Codec: "sowt", Timescale: AudioRate,
		Channels: 1, Bits: 16, SampleRate: AudioRate,
		Data: audioBytes, DefaultSampleSize: 2, SampleCount: AudioCount, Delta: 1,
	}
	return g, nil
}

func frameImage(i int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			// Deterministic moving diagonal pattern; every output pixel is touched.
			r := uint8((x*13 + i*7) & 0xff)
			g := uint8((y*17 + i*11) & 0xff)
			b := uint8((x*5 + y*19 + i*3) & 0xff)
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	return img
}
