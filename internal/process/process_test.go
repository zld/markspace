package process_test

import (
	"encoding/binary"
	"math"
	"testing"

	"mediaprep/internal/model"
	"mediaprep/internal/process"
	"mediaprep/internal/process/fingerprint"
	"mediaprep/internal/source"
)

func TestCutUsesSharedRationalBoundariesAndSnapsForward(t *testing.T) {
	track := &source.Track{Label: "mp4:1", Role: "video", Clock: model.NewClock(1, 12000)}
	for i := 0; i < 6; i++ {
		track.Samples = append(track.Samples, source.Sample{
			Index: i, DTS: int64(i * 2000), Duration: 2000,
			Payload: []byte{byte(i), byte(i + 1)},
		})
	}
	res, err := process.PlanCuts(track, process.CutRequest{
		Clock: process.DeliveryClock, CutTicks: []int64{12000, 33600},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 3 {
		t.Fatalf("segments=%d want 3: %+v", len(res.Segments), res.Segments)
	}
	if len(res.Snaps) != 2 {
		t.Fatalf("snaps=%d want 2", len(res.Snaps))
	}
	if res.Segments[0].Interval.End != 16000 || res.Segments[1].Interval.End != 40000 {
		t.Fatalf("unexpected shared-clock boundaries: %+v", res.Segments)
	}
	if res.Segments[0].EndSample != 2 || res.Segments[1].FirstSample != 2 || res.Segments[1].EndSample != 5 {
		t.Fatalf("sample alignment wrong: %+v", res.Segments)
	}
	for _, snap := range res.Snaps {
		if snap.DeviationTicks <= 0 {
			t.Fatalf("expected positive forward snap deviation: %+v", snap)
		}
	}
}

func TestSilenceThresholdAndMinimumDuration(t *testing.T) {
	pcm := makePCMWithSilence()
	track := &source.Track{
		Label: "mxf:1", Role: "audio", Codec: "pcm_s16le",
		Clock: model.NewClock(1, 48000),
		PCM:   &source.PCMFormat{Channels: 1, Bits: 16, SampleRate: 48000},
		Samples: []source.Sample{{
			Index: 0, DTS: 0, Duration: 48000, Payload: pcm,
		}},
	}
	got, err := process.DetectSilence(track, process.DefaultSilenceConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("silence intervals=%d want 1", len(got))
	}
	s := got[0]
	if s.Interval.Start != 12000 || s.Interval.End != 36000 {
		t.Fatalf("interval=%v want [12000,36000)", s.Interval)
	}
	if s.RMSDB > -60 {
		t.Fatalf("RMS %.2f dBFS does not meet -60 threshold", s.RMSDB)
	}
	if s.DurationMS < 250 {
		t.Fatalf("duration %.1f ms below minimum", s.DurationMS)
	}
}

func TestSilenceRejectsCompressedTrack(t *testing.T) {
	track := &source.Track{Label: "mp4:9", Codec: "mp4a", Role: "audio"}
	_, err := process.DetectSilence(track, process.DefaultSilenceConfig())
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeUnsupportedCodec {
		t.Fatalf("want unsupported codec error, got %v", err)
	}
}

func TestFingerprintIgnoresMetadataButNotRoleOrBytes(t *testing.T) {
	payloads := [][]byte{[]byte("first"), make([]byte, 64), []byte("last")}
	mk := func(role string, rotation string) *source.Track {
		tr := &source.Track{Label: "track-" + rotation, Role: role}
		for i, p := range payloads {
			tr.Samples = append(tr.Samples, source.Sample{Index: i, Payload: p})
		}
		tr.RawFields = []source.RawField{{Name: "rotation", Value: rotation}}
		return tr
	}
	a := mk("video", "90")
	b := mk("video", "270")
	fa, _ := fingerprint.TrackFingerprint(a)
	fb, _ := fingerprint.TrackFingerprint(b)
	if fa != fb {
		t.Fatal("metadata changed material byte fingerprint")
	}
	c := mk("audio", "90")
	fc, _ := fingerprint.TrackFingerprint(c)
	if fa == fc {
		t.Fatal("role did not participate in fingerprint")
	}
	groups := fingerprint.Dedup([]fingerprint.SourcedTrack{
		{Path: "a.mp4", Track: a}, {Path: "b.mp4", Track: b},
	})
	if len(groups) != 1 || len(groups[0].Refs) != 2 {
		t.Fatalf("dedup groups=%+v", groups)
	}
	if fingerprint.CountDuplicates(groups) != 1 {
		t.Fatalf("duplicates=%d want 1", fingerprint.CountDuplicates(groups))
	}
}

func makePCMWithSilence() []byte {
	const rate = 48000
	const loud = 12000
	const quiet = 24000
	total := loud + quiet + loud
	out := make([]byte, total*2)
	for n := 0; n < total; n++ {
		var v int16
		if n < loud || n >= loud+quiet {
			v = int16(12000 * math.Sin(2*math.Pi*440*float64(n)/rate))
		} else {
			v = 0
		}
		binary.LittleEndian.PutUint16(out[n*2:], uint16(v))
	}
	return out
}
