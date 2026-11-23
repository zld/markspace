package manifest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMakeSegmentsMergesAdjacent(t *testing.T) {
	events := []Event{
		{Seq: 1, StartSample: 0, EndSample: 960},
		{Seq: 2, StartSample: 960, EndSample: 1920},
		{Seq: 3, Status: StatusMissing, StartSample: 1920, EndSample: 2880},
		{Seq: 4, Status: StatusReplayed, StartSample: 2880, EndSample: 3840},
	}
	segs := MakeSegments(48000, events)
	if len(segs) != 3 {
		t.Fatalf("segments = %+v", segs)
	}
	if segs[0].Status != StatusOK || segs[0].SeqEnd != 2 || segs[0].EndSample != 1920 {
		t.Fatalf("first = %+v", segs[0])
	}
	if segs[1].Status != StatusMissing || segs[2].Status != StatusReplayed {
		t.Fatalf("statuses = %s %s", segs[1].Status, segs[2].Status)
	}
	if segs[0].EndSeconds != 1920.0/48000 {
		t.Fatalf("seconds = %f", segs[0].EndSeconds)
	}
}

func TestWriteReadStable(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	tl := Timeline{
		SampleRate: 48000, Channels: 2, RTPClockRate: 48000,
		DurationSample: 1920, MediaFile: "out.mp4",
		Segments: MakeSegments(48000, []Event{{Seq: 1, StartSample: 0, EndSample: 1920}}),
		Missing:  []Range{{StartSample: 480, EndSample: 960}},
	}
	if err := Write(a, tl); err != nil {
		t.Fatal(err)
	}
	if err := Write(b, tl); err != nil {
		t.Fatal(err)
	}
	da, _ := os.ReadFile(a)
	db, _ := os.ReadFile(b)
	if string(da) != string(db) {
		t.Fatal("manifest output is not deterministic")
	}
	got, err := Read(a)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Segments, tl.Segments) || got.Version != 1 {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestReadRejectsBadJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("expected parse error")
	}
}
