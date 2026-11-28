package mp4

import (
	"bytes"
	"testing"

	gomp4 "github.com/abema/go-mp4"

	"medcheck/internal/model"
)

func buildMP4(t *testing.T, n int) []byte {
	t.Helper()
	b := NewBuilder(1, 90000)
	for i := 0; i < n; i++ {
		payload := []byte{byte(i), byte(i + 1), byte(i + 2), byte(i + 3)}
		b.AddSample(SampleSpec{
			Payload: payload,
			Delta:   3000,
			IsKey:   i%3 == 0,
		})
	}
	data, err := b.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return data
}

func TestBuildScanRoundTrip(t *testing.T) {
	data := buildMP4(t, 6)
	scan, err := ParseBytes(data, "rt")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(scan.Errors) != 0 {
		t.Fatalf("unexpected scan errors: %+v", scan.Errors)
	}
	if len(scan.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(scan.Tracks))
	}
	tr := scan.Tracks[0]
	if tr.TrackID != 1 || tr.Timescale.Num != 90000 {
		t.Fatalf("track = %+v", tr)
	}
	if len(tr.Samples) != 6 {
		t.Fatalf("samples = %d", len(tr.Samples))
	}
	wantKeys := []uint32{0, 3}
	if len(tr.KeyFrames) != len(wantKeys) {
		t.Fatalf("keyframes = %v want %v", tr.KeyFrames, wantKeys)
	}
	for i, k := range wantKeys {
		if tr.KeyFrames[i] != k {
			t.Fatalf("keyframe %d = %d want %d", i, tr.KeyFrames[i], k)
		}
	}
	// Offsets must be ascending, point into mdat, and ticks cumulative.
	var prev int64 = -1
	for i, s := range tr.Samples {
		if s.Offset <= prev {
			t.Fatalf("sample %d offset not ascending: %d", i, s.Offset)
		}
		if s.Offset < 0 || s.Offset >= int64(len(data)) {
			t.Fatalf("sample %d offset out of stream: %d", i, s.Offset)
		}
		if s.Ticks != int64(i)*3000 {
			t.Fatalf("sample %d ticks = %d", i, s.Ticks)
		}
		prev = s.Offset
	}
}

func TestGoMp4RescanWithLibrary(t *testing.T) {
	data := buildMP4(t, 3)
	r := bytes.NewReader(data)
	count := 0
	_, err := gomp4.ReadBoxStructure(r, func(h *gomp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type == gomp4.BoxTypeMdat() {
			count++
		}
		return h.Expand()
	})
	if err != nil {
		t.Fatalf("library rescan: %v", err)
	}
	if count != 1 {
		t.Fatalf("mdat boxes = %d want 1", count)
	}
}

func TestTruncatedTopLevelBoxLocated(t *testing.T) {
	data := buildMP4(t, 2)
	// Insert a fresh box header after the last complete box declaring a size
	// larger than the bytes actually present.
	trunc := append([]byte{}, data...)
	insAt := int64(len(trunc))
	trunc = append(trunc, 0, 0, 0, 40, 'm', 'd', 'a', 't')
	trunc = append(trunc, 1, 2, 3) // far fewer than 40-8 bytes
	scan, err := ParseBytes(trunc, "rt")
	if err != nil {
		t.Fatalf("truncation must be recoverable, got %v", err)
	}
	if len(scan.Errors) == 0 {
		t.Fatal("expected a truncated box error")
	}
	var found bool
	var pos int64 = -1
	for _, ce := range scan.Errors {
		if ce.Kind == model.KindTruncatedBox {
			found = true
			pos = ce.FirstPos
		}
	}
	if !found {
		t.Fatalf("errors = %+v", scan.Errors)
	}
	if pos != insAt {
		t.Fatalf("truncation pos = %d want %d", pos, insAt)
	}
	// Recovered earlier boxes remain present.
	if len(scan.Boxes) < 3 {
		t.Fatalf("recovered boxes = %d", len(scan.Boxes))
	}
}

func TestTruncatedHeaderAtEOF(t *testing.T) {
	data := buildMP4(t, 1)
	cut := append([]byte{}, data...)
	cut = append(cut, 0, 0) // partial next header
	scan, err := ParseBytes(cut, "rt")
	if err != nil {
		t.Fatalf("partial header should not hard fail: %v", err)
	}
	if len(scan.Errors) != 1 || scan.Errors[0].Kind != model.KindTruncatedBox {
		t.Fatalf("errors = %+v", scan.Errors)
	}
}

func TestSampleByteContentMatchesOffsets(t *testing.T) {
	data := buildMP4(t, 4)
	scan, err := ParseBytes(data, "rt")
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range scan.Tracks[0].Samples {
		got := data[s.Offset : s.Offset+int64(s.Size)]
		want := []byte{byte(i), byte(i + 1), byte(i + 2), byte(i + 3)}
		if !bytes.Equal(got, want) {
			t.Fatalf("sample %d bytes = %v want %v", i, got, want)
		}
	}
}
