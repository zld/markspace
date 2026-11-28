package fixture

import (
	"os"
	"path/filepath"
)

// Standard fixture file names inside a generated case directory.
const (
	MainMP4       = "main.mp4"
	TransitionMP4 = "transition.mp4"
	VideoRTP      = "video.rtp"
	AudioRTP      = "audio.rtp"
)

// VideoDeltas returns the VFR video sample durations (90 kHz ticks).
func VideoDeltas() []uint32 {
	d := make([]uint32, 50)
	for i := range d {
		d[i] = 1800 + uint32((int(i*7)%5-2)*150) // 1500..2100, deterministic
	}
	return d
}

// AudioDeltas returns the audio sample durations (48 kHz ticks, 20 ms).
func AudioDeltas() []uint32 {
	d := make([]uint32, 50)
	for i := range d {
		d[i] = 960
	}
	return d
}

// TransitionDeltas returns the transition-clip durations (1 kHz ticks).
func TransitionDeltas() []uint32 {
	d := make([]uint32, 10)
	for i := range d {
		d[i] = 100
	}
	return d
}

// Generate writes the full deterministic fixture set into dir.
// damaged replaces the final audio sample's media bytes with nothing,
// leaving its declared extent outside the mdat payload.
func Generate(dir string, damaged bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	video := TrackSpec{
		Handler: "vide", Timescale: 90000,
		Deltas: VideoDeltas(), CTTS: make([]int32, len(VideoDeltas())),
	}
	audio := TrackSpec{
		Handler: "soun", Timescale: 48000, Deltas: AudioDeltas(),
	}
	if damaged {
		last := len(AudioDeltas()) - 1
		audio.PayloadOf = func(i int) int {
			if i == last {
				return 0 // truncated: declared but never arrived
			}
			return int(DefaultSize(i))
		}
	}
	if err := WriteMP4(filepath.Join(dir, MainMP4), []TrackSpec{video, audio}); err != nil {
		return err
	}
	transition := TrackSpec{Handler: "meta", Timescale: 1000, Deltas: TransitionDeltas()}
	if err := WriteMP4(filepath.Join(dir, TransitionMP4), []TrackSpec{transition}); err != nil {
		return err
	}
	// Video RTP: sequence and timestamp both start near the wrap point;
	// one retransmitted loss, one duplicate, one reorder.
	if err := WriteRTP(filepath.Join(dir, VideoRTP), 65530, 0xFFFFFFF0, 0x51,
		VideoDeltas(), RTPEdit{Late: []int{12}, LateBy: 5, Dup: []int{20}, Swap: [][2]int{{30, 31}}}); err != nil {
		return err
	}
	// Audio RTP: one retransmitted loss, one duplicate.
	return WriteRTP(filepath.Join(dir, AudioRTP), 7, 0x1000, 0xA1,
		AudioDeltas(), RTPEdit{Late: []int{40}, LateBy: 3, Dup: []int{5}})
}
