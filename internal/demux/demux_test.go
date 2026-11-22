package demux

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"mediasync/internal/ebml"
	"mediasync/internal/testmedia"
)

func healthySpec() testmedia.Spec {
	return testmedia.Spec{
		Seed: 41, Tracks: []string{"video", "audio", "subtitle"},
		Clusters: 4, BlocksPerCluster: 3,
	}
}

func TestDemuxRoundTrip(t *testing.T) {
	data, truth := testmedia.BuildWebM(healthySpec())
	d, err := New(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.ScaleNS() != truth.ScaleNS {
		t.Fatalf("scale = %d want %d", d.ScaleNS(), truth.ScaleNS)
	}
	var got []*Sample
	for {
		s, err := d.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, s)
	}
	if len(got) != len(truth.Blocks) {
		t.Fatalf("blocks = %d want %d", len(got), len(truth.Blocks))
	}
	for i, s := range got {
		want := truth.Blocks[i]
		if s.Track != want.Track || s.Seq != want.Seq || s.TimeTicks != want.AbsTick {
			t.Fatalf("sample %d mismatch: got %+v want %+v", i, s, want)
		}
		if !bytes.Equal(s.Payload, want.Payload) {
			t.Fatalf("sample %d payload altered (re-encoding?)", i)
		}
	}
	sum := d.Summary()
	if sum.TotalBlocks != len(truth.Blocks) || sum.Truncated != nil {
		t.Fatalf("summary wrong: %+v", sum)
	}
}

func TestUnknownLengthSegment(t *testing.T) {
	data, truth := testmedia.BuildWebM(healthySpec())
	damaged := testmedia.ApplyDamage(data, testmedia.DamageUnknownSegment)
	d, err := New(bytes.NewReader(damaged))
	if err != nil {
		t.Fatalf("New on unknown segment: %v", err)
	}
	n := 0
	for {
		_, err := d.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		n++
	}
	if n != len(truth.Blocks) {
		t.Fatalf("unknown-length segment blocks = %d want %d", n, len(truth.Blocks))
	}
}

func TestTruncatedClusterReportsOffsetAndTrack(t *testing.T) {
	data, _ := testmedia.BuildWebM(healthySpec())
	damaged := testmedia.ApplyDamage(data, testmedia.DamageTruncate)
	d, err := New(bytes.NewReader(damaged))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var sawTrunc bool
	for {
		_, err := d.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var ee *ebml.Error
			if errors.As(err, &ee) && ee.Kind == ebml.KindTruncated {
				sawTrunc = true
				if ee.Offset <= 0 || ee.Offset > int64(len(damaged)) {
					t.Fatalf("bad truncation offset %d", ee.Offset)
				}
			}
			break
		}
	}
	if !sawTrunc {
		t.Fatal("truncated fixture must surface KindTruncated with offset")
	}
	sum := d.Summary()
	if sum.Truncated == nil {
		t.Fatal("summary must retain the truncation diagnostic")
	}
	if sum.TotalBlocks == 0 || sum.TotalBlocks >= len(data) {
		t.Fatalf("partial samples should remain counted, got %d", sum.TotalBlocks)
	}
}

func TestTrackDeclarations(t *testing.T) {
	data, _ := testmedia.BuildWebM(healthySpec())
	d, err := New(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tracks := d.Tracks()
	if len(tracks) != 3 {
		t.Fatalf("tracks = %d", len(tracks))
	}
	want := map[uint32]string{1: "V_VP9", 2: "A_OPUS", 3: "S_TEXT/WEBVTT"}
	for _, tr := range tracks {
		if tr.CodecID != want[tr.Number] {
			t.Fatalf("track %d codec = %q", tr.Number, tr.CodecID)
		}
	}
}

func TestMappingValidation(t *testing.T) {
	data, _ := testmedia.BuildWebM(healthySpec())
	d, err := New(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tracks := d.Tracks()

	ok := []Binding{{Source: 1, Role: RoleVideo}, {Source: 2, Role: RoleAudio}, {Source: 3, Role: RoleSubtitle}}
	m, err := Resolve(tracks, ok)
	if err != nil {
		t.Fatalf("valid mapping rejected: %v", err)
	}
	if got := m.Sources(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("sources order: %v", got)
	}

	if _, err := Resolve(tracks, []Binding{{Source: 99, Role: RoleVideo}}); err == nil {
		t.Fatal("missing source must fail")
	} else if me, ok := err.(*MapError); !ok || me.Kind != KindMissingTrack {
		t.Fatalf("want KindMissingTrack, got %v", err)
	}

	dup := []Binding{{Source: 1, Role: RoleVideo}, {Source: 2, Role: RoleVideo}}
	if _, err := Resolve(tracks, dup); err == nil {
		t.Fatal("duplicate role must fail")
	} else if me, ok := err.(*MapError); !ok || me.Kind != KindDuplicateTarget {
		t.Fatalf("want KindDuplicateTarget, got %v", err)
	}

	// Audio track bound to video role: incompatible codec.
	if _, err := Resolve(tracks, []Binding{{Source: 2, Role: RoleVideo}}); err == nil {
		t.Fatal("incompatible codec/role must fail")
	} else if me, ok := err.(*MapError); !ok || me.Kind != KindIncompatibleCodec {
		t.Fatalf("want KindIncompatibleCodec, got %v", err)
	}
}

func TestClockConfigsFromMapping(t *testing.T) {
	data, _ := testmedia.BuildWebM(healthySpec())
	d, _ := New(bytes.NewReader(data))
	m, err := Resolve(d.Tracks(), []Binding{{Source: 1, Role: RoleVideo}})
	if err != nil {
		t.Fatal(err)
	}
	cfgs := m.ClockConfigs(900000, map[uint32]float64{1: 180})
	if len(cfgs) != 1 || cfgs[0].Track != 1 || cfgs[0].WrapAt != 900000 ||
		cfgs[0].DriftPPM != 180 || cfgs[0].TicksPerSecond != 1000 {
		t.Fatalf("clock config wrong: %+v", cfgs)
	}
}

func TestRejectsUndeclaredTrackBlock(t *testing.T) {
	data, _ := testmedia.BuildWebM(healthySpec())
	// Find a real block structurally: SimpleBlock ID, size VINT, then a
	// track VINT in the valid 0x81..0x83 range (generated tracks 1..3).
	idx := -1
	for i := 0; i+4 < len(data); i++ {
		if data[i] == 0xA3 && data[i+1]&0x80 != 0 && data[i+1] != 0xFF &&
			data[i+2] >= 0x81 && data[i+2] <= 0x83 {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("no SimpleBlock found")
	}
	corrupt := append([]byte(nil), data...)
	corrupt[idx+2] = 0x89 // 1-byte VINT encoding undeclared track 9
	d, err := New(bytes.NewReader(corrupt))
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err := d.Next()
		if errors.Is(err, io.EOF) {
			t.Fatal("undeclared track block must be rejected")
		}
		if err != nil {
			return // expected
		}
	}
}
