package media

import (
	"bytes"
	"testing"
)

func probeFixture(t *testing.T) *Fixture {
	t.Helper()
	fx, err := BuildFixture(FixtureOptions{
		Seed: 41,
		Tracks: []FixtureTrack{
			{ID: 1, Codec: "avc1", Lang: "eng", Timescale: 90000, Samples: 24, SyncEvery: 3, Width: 640, Height: 360},
			{ID: 2, Codec: "mp4a", Lang: "eng", Timescale: 48000, Samples: 30},
		},
	})
	if err != nil {
		t.Fatalf("build fixture: %v", err)
	}
	return fx
}

func TestProbeValidFixture(t *testing.T) {
	fx := probeFixture(t)
	p, err := ProbeFile(bytes.NewReader(fx.Bytes), int64(len(fx.Bytes)), "fixture.mp4")
	if err != nil {
		t.Fatalf("ProbeFile: %v", err)
	}
	if p.Brand != "isom" {
		t.Errorf("brand = %q, want isom", p.Brand)
	}
	if p.Timescale != 1000 {
		t.Errorf("timescale = %d, want 1000", p.Timescale)
	}
	if len(p.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(p.Tracks))
	}
	video := p.Tracks[0]
	if video.TrackID != 1 || video.Codec != "avc1" || video.Handler != "vide" {
		t.Errorf("video track = %+v", video)
	}
	if video.Language != "eng" {
		t.Errorf("language = %q, want eng", video.Language)
	}
	if video.SampleCount != 24 {
		t.Errorf("video samples = %d, want 24", video.SampleCount)
	}
	if len(video.Keyframes) != 8 {
		t.Errorf("video keyframes = %d, want 8 (every 3rd of 24)", len(video.Keyframes))
	}
	if len(video.Table.CTS) != 24 || len(video.Table.DTS) != 24 {
		t.Errorf("sample timing table shape = (%d,%d)", len(video.Table.CTS), len(video.Table.DTS))
	}
	audio := p.Tracks[1]
	if audio.Codec != "mp4a" || audio.Handler != "soun" {
		t.Errorf("audio track = %+v", audio)
	}
	if len(audio.Keyframes) != 30 {
		t.Errorf("audio keyframes = %d, want 30 (no stss means all sync)", len(audio.Keyframes))
	}
	if err := ValidateMovieBounds(p); err != nil {
		t.Errorf("ValidateMovieBounds: %v", err)
	}
	// Fast-start is false for the generated layout (moov before mdat if built
	// that way); BuildFile always writes moov before mdat.
	if !p.FastStart {
		t.Errorf("fixture should be fast-start (moov before mdat)")
	}
}

func TestProbeEditList(t *testing.T) {
	fx, err := BuildFixture(FixtureOptions{
		Seed: 7,
		Tracks: []FixtureTrack{
			{ID: 1, Codec: "avc1", Lang: "eng", Timescale: 90000, Samples: 6, SyncEvery: 3,
				Edits: []EditEntry{{SegmentDuration: 1000, MediaTime: 0, Rate: 1}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProbeFile(bytes.NewReader(fx.Bytes), int64(len(fx.Bytes)), "edit.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if p.EditCount != 1 {
		t.Fatalf("edit count = %d, want 1", p.EditCount)
	}
	eds := p.Tracks[0].Edits
	if len(eds) != 1 || eds[0].SegmentDuration != 1000 || eds[0].MediaTime != 0 {
		t.Errorf("edits = %+v", eds)
	}
}

func TestProbeFaults(t *testing.T) {
	fx := probeFixture(t)
	tests := []struct {
		kind    FaultKind
		wantCls Class
	}{
		{FaultTruncatedBox, ClassBoxBounds},
		{FaultInvalidSample, ClassInvalidSample},
		{FaultUnknownBox, ClassUnsupported},
		{FaultZeroTimescale, ClassTimebase},
		{FaultDuplicateStss, ClassInvalidSample},
		{FaultBadChunkCount, ClassInvalidSample},
	}
	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			bad, off, err := Corrupt(fx.Bytes, tc.kind)
			if err != nil {
				t.Fatalf("Corrupt: %v", err)
			}
			p, err := ProbeFile(bytes.NewReader(bad), int64(len(bad)), "bad.mp4")
			if err == nil {
				if err = ValidateMovieBounds(p); err == nil {
					t.Fatalf("corrupt fixture (%s) was accepted", tc.kind)
				}
			}
			if got := ClassOf(err); got != tc.wantCls {
				t.Fatalf("class = %s (%v), want %s", got, err, tc.wantCls)
			}
			if ce, ok := AsError(err); ok && !ce.HasLocation() {
				t.Errorf("error %v has no structural offset", err)
			}
			if off < 0 || off > int64(len(bad)) {
				t.Errorf("reported corruption offset %d outside file of %d bytes", off, len(bad))
			}
		})
	}
}

func TestProbeRejectsEmptyAndOversize(t *testing.T) {
	if _, err := ProbeFile(bytes.NewReader(nil), 0, "empty"); ClassOf(err) != ClassTruncated {
		t.Errorf("empty input class = %s, want truncated", ClassOf(err))
	}
	if _, err := ProbeFile(bytes.NewReader([]byte("x")), MaxInputSize+1, "huge"); ClassOf(err) != ClassSizeOverflow {
		t.Errorf("oversize input class = %s, want size_overflow", ClassOf(err))
	}
}

func TestScanRejectsSizeOverflow(t *testing.T) {
	// A box claiming 1<<62 bytes must be rejected before any allocation.
	raw := make([]byte, 16)
	raw[0], raw[1], raw[2], raw[3] = 0, 0, 0, 1
	copy(raw[4:8], "moov")
	raw[15] = 0xff
	_, err := ScanBoxes(bytes.NewReader(raw), int64(len(raw)))
	if err == nil {
		t.Fatal("oversized box accepted")
	}
	if ClassOf(err) != ClassSizeOverflow && ClassOf(err) != ClassBoxBounds {
		t.Errorf("class = %s, want size_overflow or box_bounds", ClassOf(err))
	}
}

func TestCorruptNeverMutatesInput(t *testing.T) {
	fx := probeFixture(t)
	for _, kind := range []FaultKind{FaultTruncatedBox, FaultInvalidSample, FaultUnknownBox} {
		orig := append([]byte(nil), fx.Bytes...)
		if _, _, err := Corrupt(fx.Bytes, kind); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if !bytes.Equal(orig, fx.Bytes) {
			t.Fatalf("%s mutated the source fixture", kind)
		}
	}
}
