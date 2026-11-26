package fixture

import (
	"encoding/binary"
	"math"
	"path/filepath"
)

type DemoFiles struct {
	Root      string
	MainVideo string
	Duplicate string
	AudioMXF  string
	CutTicks  []int64
}

// GenerateDemoFiles creates deterministic local MP4/MXF material. The main and
// duplicate video files have byte-identical sample payloads but different
// metadata assertions.
func GenerateDemoFiles(root string) (*DemoFiles, error) {
	files := &DemoFiles{
		Root:      root,
		MainVideo: filepath.Join(root, "media", "video-main.mp4"),
		Duplicate: filepath.Join(root, "media", "video-duplicate.mp4"),
		AudioMXF:  filepath.Join(root, "media", "audio.mxf"),
		// Shared 48 kHz ticks: 250 ms and 700 ms. Both fall inside 166.7 ms
		// MP4 samples and therefore snap forward to the next sample boundary.
		CutTicks: []int64{12000, 33600},
	}
	payloads := demoVideoPayloads()
	mainMeta := map[string]string{
		"rotation": "90",
		"crop":     "10,8,300,164",
		"color":    "bt709/primaries",
	}
	mainTrack := map[string]string{
		"rotation": "180",
		"crop":     "0,0,320,180",
		"color":    "bt601/track",
	}
	mainContainer := map[string]string{
		"rotation": "0",
		"color":    "bt2020/container",
	}
	if _, err := WriteMP4(MP4Spec{
		Path: files.MainVideo, Width: 320, Height: 180, Payloads: payloads,
		SampleFields: mainMeta, TrackFields: mainTrack, ContainerFields: mainContainer,
	}); err != nil {
		return nil, err
	}

	// Same payloads in the same order; only metadata differs.
	if _, err := WriteMP4(MP4Spec{
		Path: files.Duplicate, Width: 320, Height: 180, Payloads: payloads,
		SampleFields: map[string]string{"rotation": "270", "color": "p3/different-metadata"},
	}); err != nil {
		return nil, err
	}

	pcm := demoPCM()
	if _, err := WriteMXF(MXFSpec{Path: files.AudioMXF, PCM: pcm}); err != nil {
		return nil, err
	}
	return files, nil
}

func demoVideoPayloads() [][]byte {
	payloads := make([][]byte, 0, 6)
	for i := 0; i < 6; i++ {
		b := make([]byte, 60000)
		binary.BigEndian.PutUint32(b[0:4], uint32(i))
		for j := 4; j < len(b); j++ {
			b[j] = byte(i*37 + j*17)
		}
		payloads = append(payloads, b)
	}
	return payloads
}

func demoPCM() []byte {
	const rate = 48000
	const total = 57600 // 1.2 s
	firstTone := 16800  // 350 ms
	silence := 24000    // 500 ms
	out := make([]byte, total*2)
	for n := 0; n < total; n++ {
		var v int16
		switch {
		case n < firstTone || n >= firstTone+silence:
			v = int16(12000 * math.Sin(2*math.Pi*440*float64(n)/rate))
		default:
			// Deterministic one-LSB dither keeps dBFS finite and far below -60.
			if n%2 == 0 {
				v = 1
			} else {
				v = -1
			}
		}
		binary.LittleEndian.PutUint16(out[n*2:n*2+2], uint16(v))
	}
	return out
}
