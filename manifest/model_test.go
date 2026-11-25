package manifest

import (
	"testing"

	"github.com/zld/media-regress/media"
)

func sampleTarget() AudioTarget {
	return AudioTarget{SampleRate: 48000, Layout: "stereo", Channels: 2, LoudnessLUFS: "-16", GainDB: "-3.0"}
}

func videoTrack() *media.Track {
	return &media.Track{
		ID: 1, Kind: media.KindVideo, Timescale: 25, Duration: 50,
		Video: &media.VideoInfo{Width: 1920, Height: 1080, Rotation: 90},
	}
}

func audioTrack() *media.Track {
	return &media.Track{
		ID: 2, Kind: media.KindAudio, Timescale: 48000, Duration: 96000,
		Audio: &media.AudioInfo{SampleRate: 48000, Channels: 2, Layout: "stereo"},
	}
}

func sampleManifest(t *testing.T) *Manifest {
	t.Helper()
	b := NewBuilder("valid.mp4", FormatMP4, "input-digest")
	b.SetAudioTarget(sampleTarget())
	if err := b.AddTrack(videoTrack()); err != nil {
		t.Fatal(err)
	}
	if err := b.AddTrack(audioTrack()); err != nil {
		t.Fatal(err)
	}
	// 12 contiguous 0.25 s segments at 90 kHz.
	for i := 1; i <= 12; i++ {
		start := int64(i-1) * 15000
		b.AddSegment(Segment{
			Index: i, TrackID: 2, StartTick: start, EndTick: start + 15000,
			Frames: 12000, Digest: SegmentDigest([]byte{byte(i)}), Bytes: 48000,
		})
	}
	b.SetAudioWindow()
	if err := b.Finish(Summary{Stages: 5}); err != nil {
		t.Fatal(err)
	}
	return b.Manifest()
}
