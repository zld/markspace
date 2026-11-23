package adapter

import "testing"

type stubParser struct {
	desc Capability
}

func (s stubParser) Descriptor() Capability      { return s.desc }
func (s stubParser) Supports(f string) bool      { return s.desc.Supports(f) }

func TestRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	p := stubParser{Capability{Kind: KindHLS, Name: "m3u8", Version: "1.0.0",
		Features: []string{"discontinuity", "byte-range"}}}
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(p); err == nil {
		t.Fatal("duplicate registration must be rejected")
	}
	got, err := r.Lookup(KindHLS, "m3u8", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Supports("discontinuity") || got.Supports("fec-xor") {
		t.Fatal("capability support check wrong")
	}
}

func TestRegisterRejectsEmptyDescriptor(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(stubParser{Capability{Kind: KindRTP}}); err == nil {
		t.Fatal("empty name/version must be rejected")
	}
}

func TestNegotiatePicksHighestVersion(t *testing.T) {
	r := NewRegistry()
	old := stubParser{Capability{Kind: KindRTP, Name: "rtp", Version: "1.2.0",
		Features: []string{"fec-xor"}}}
	new := stubParser{Capability{Kind: KindRTP, Name: "rtp", Version: "1.10.1",
		Features: []string{"fec-xor", "rtcp"}}}
	other := stubParser{Capability{Kind: KindHLS, Name: "m3u8", Version: "9.0.0",
		Features: []string{"fec-xor"}}}
	for _, p := range []Parser{old, new, other} {
		if err := r.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.Negotiate(KindRTP, []string{"fec-xor"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Descriptor().Version != "1.10.1" {
		t.Fatalf("negotiated %s, want 1.10.1", got.Descriptor().Version)
	}
	if _, err := r.Negotiate(KindRTP, []string{"av1"}); err == nil {
		t.Fatal("unsupported feature set must fail negotiation")
	}
	feats := r.Features(KindRTP)
	if len(feats) != 2 || feats[0] != "fec-xor" {
		t.Fatalf("feature union wrong: %v", feats)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.0", "1.10.1", -1},
		{"2.0", "2.0.0", 0},
		{"3.1.2", "3.1.1", 1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s,%s)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestDefaultLimitsBounded(t *testing.T) {
	l := DefaultLimits()
	if l.MaxSegments <= 0 || l.MaxBytes <= 0 || l.MaxDepth <= 0 ||
		l.MaxTracks <= 0 || l.MaxWindowSize <= 0 {
		t.Fatal("all default limits must be positive and bounded")
	}
}
