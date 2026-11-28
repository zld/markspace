package manifest

import (
	"strings"
	"testing"

	"mediacheck/internal/fault"
)

func sampleV1() *Manifest {
	return &Manifest{
		Codecs:   []string{"h264", "h265", "av1"},
		Timebase: 90000,
		Segments: []SegmentEntry{{Codec: "h264", Index: 0, StartTick: 0, EndTick: 9000,
			SHA256: strings.Repeat("a", 64), Packets: 4}},
		Frames:  []FrameEntry{{Codec: "h264", Index: 0, SHA256: strings.Repeat("b", 64), Min: 0, Max: 255, Mean: 127.5}},
		Quality: Quality{LoudnessPeakDBFS: -6.02, MaxAlignError: 0.5},
	}
}

func TestRoundTripV1(t *testing.T) {
	data, err := Marshal(sampleV1())
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 || m.Timebase != 90000 || m.Quality.LoudnessPeakDBFS != -6.02 {
		t.Fatalf("%+v", m)
	}
	if len(m.Segments) != 1 || len(m.Segments[0].SHA256) != 64 {
		t.Fatalf("%+v", m.Segments)
	}
}

func TestParseV0Upgrades(t *testing.T) {
	data, err := MarshalV0(&V0{Codecs: []string{"h264"},
		Segments: []SegmentEntry{{Codec: "h264", Index: 0, SHA256: strings.Repeat("c", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 0 || len(m.Segments) != 1 || m.Timebase != 0 {
		t.Fatalf("%+v", m)
	}
}

func TestRejectUnknownField(t *testing.T) {
	data, _ := Marshal(sampleV1())
	s := strings.Replace(string(data), `"timebase"`, `"timezone"`, 1)
	if _, err := Parse([]byte(s)); !fault.HasCode(err, fault.ErrUnknownField) {
		t.Fatalf("got %v", err)
	}
}

func TestRejectUnknownVersion(t *testing.T) {
	if _, err := Parse([]byte(`{"version": 7}`)); !fault.HasCode(err, fault.ErrBadVersion) {
		t.Fatalf("got %v", err)
	}
}

func TestRejectGarbage(t *testing.T) {
	if _, err := Parse([]byte(`not json`)); !fault.HasCode(err, fault.ErrUnknownField) {
		t.Fatalf("got %v", err)
	}
}

func TestMarshalDeterministic(t *testing.T) {
	a, _ := Marshal(sampleV1())
	b, _ := Marshal(sampleV1())
	if string(a) != string(b) {
		t.Fatal("manifest bytes differ between runs")
	}
}
