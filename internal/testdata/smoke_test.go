package testdata

import (
	"os"
	"testing"
)

func TestSmokeFixtures(t *testing.T) {
	dir := t.TempDir()
	audio := NewAudio(42, 48000, 4_000_000_000)
	frames := NewFrames(42, 90, 64, 48, 12)
	r := NewRand(42)
	pts, dts, dur := VFRTiming(r, len(frames), 1_000_000_000/30, 500_000_000, 7)
	spec := MP4Spec{
		Seed: 42, Width: 64, Height: 48, Timescale: 30000,
		Audio: audio, Frames: frames, VideoPTS: pts, VideoDTS: dts, VideoDur: dur,
		StartNanos: 500_000_000, Rotation: 90, Crop: [4]int64{2, 2, 2, 2},
		Colors: []ColorSpec{{9, 16, 9, true}, {1, 1, 1, false}},
	}
	mp4Path := dir + "/clip.mp4"
	if err := WriteMP4(mp4Path, spec); err != nil {
		t.Fatal(err)
	}
	mxfPath := dir + "/clip.mxf"
	if err := WriteMXF(mxfPath, MXFSpec{
		Seed: 42, Timescale: 30000, Rotation: 180, FrameCount: 30,
		FrameSize: 128, KeyEvery: 10, UnknownKLV: true,
		Colors: []ColorSpec{{9, 16, 9, true}},
	}); err != nil {
		t.Fatal(err)
	}
	pkts, late := BuildRTPPackets(RTPWrapSpec{
		ClockRate: 48000, PacketSteps: 960, StartTS: 0xFFFFF800,
		DriftPPM: 180, Packets: 40, Wrap: true, ReorderIndex: 5, LateIndex: 38,
	})
	rtpPath := dir + "/capture.rtp"
	if err := WriteRTP(rtpPath, pkts, late); err != nil {
		t.Fatal(err)
	}
	if off, err := TruncateBox(mp4Path, dir+"/trunc.mp4"); err != nil || off < 0 {
		t.Fatalf("truncate box: %v off=%d", err, off)
	}
	if off, err := TruncateKLV(mxfPath, dir+"/trunc.mxf"); err != nil || off < 0 {
		t.Fatalf("truncate klv: %v off=%d", err, off)
	}
	t.Logf("files written in %s", dir)
	for _, name := range []string{"clip.mp4", "clip.mxf", "capture.rtp", "trunc.mp4", "trunc.mxf"} {
		fi, err := os.Stat(dir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s %d bytes", name, fi.Size())
	}
}
