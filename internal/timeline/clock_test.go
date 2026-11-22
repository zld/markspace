package timeline

import "testing"

func TestParseRate(t *testing.T) {
	cases := []struct {
		in   string
		want Rate
	}{
		{"24", Rate24}, {"25", Rate25}, {"50", Rate50},
		{"29.97", Rate2997}, {"29.97ndf", Rate2997}, {"29.97df", Rate2997DF},
		{"30000/1001", Rate2997}, {"30", Rate{Num: 30, Den: 1}},
	}
	for _, c := range cases {
		got, err := ParseRate(c.in)
		if err != nil {
			t.Fatalf("ParseRate(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseRate(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
	// 60fps cannot be expressed with whole-nanosecond frame durations.
	for _, bad := range []string{"23.976", "29.970", "48000/1001", "60", "abc", ""} {
		if _, err := ParseRate(bad); err == nil {
			t.Errorf("ParseRate(%q) expected error", bad)
		}
	}
	if _, err := ParseRate("25df"); err == nil {
		t.Error("drop-frame at 25fps must be rejected")
	}
}

func TestDropFrameIllegalLabels(t *testing.T) {
	r := Rate2997DF
	good := []string{"00:00:00:02", "00:01:00:02", "00:10:00:00", "01:00:00:00"}
	for _, g := range good {
		if _, err := r.Normalize(g); err != nil {
			t.Errorf("label %s should be legal: %v", g, err)
		}
	}
	for _, bad := range []string{"00:01:00:00", "00:01:00:01", "00:00:59:30", "00:00:00:30"} {
		if _, err := r.Normalize(bad); err == nil {
			t.Errorf("label %s must be rejected", bad)
		}
	}
}

func TestDropFrameCounts(t *testing.T) {
	r := Rate2997DF
	// 00:01:00:02 nominal frame 1802 minus 2 dropped labels => frame 1800.
	m, err := r.Normalize("00:01:00:02")
	if err != nil {
		t.Fatal(err)
	}
	if m.Frame != 1800 {
		t.Errorf("frame count = %d, want 1800", m.Frame)
	}
	// At the 10-minute boundary no labels are dropped during that minute,
	// but 18 frames were skipped over the first 9 minutes: 18000-18.
	m10, err := r.Normalize("00:10:00:00")
	if err != nil {
		t.Fatal(err)
	}
	if m10.Frame != 17982 {
		t.Errorf("10min frame = %d, want 17982", m10.Frame)
	}
	// First frame duration ~33.3667ms; rounded 33366667 ns, residual negative.
	m0, _ := r.Normalize("00:00:00:01")
	if m0.Nanos != 33_366_667 || m0.ResidualNanos >= 0 {
		t.Errorf("29.97 frame ns=%d residual=%d", m0.Nanos, m0.ResidualNanos)
	}
}

func TestNDFAndIntegerRates(t *testing.T) {
	m, err := Rate25.Normalize("00:00:01:00")
	if err != nil {
		t.Fatal(err)
	}
	if m.Nanos != NanosPerSecond || m.Frame != 25 || m.ResidualNanos != 0 {
		t.Errorf("25fps mapping = %+v", m)
	}
	m24, err := Rate24.Normalize("00:00:00:01")
	if err != nil {
		t.Fatal(err)
	}
	if m24.Nanos != 41_666_667 || m24.ResidualNanos >= 0 {
		t.Errorf("24fps rounding = %+v", m24)
	}
}

func TestNormalizerBackward(t *testing.T) {
	n := NewNormalizer(Rate25)
	if _, err := n.Normalize("00:00:01:00"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Normalize("00:00:00:24"); err == nil {
		t.Fatal("backward timecode must fail")
	}
	if _, err := n.Normalize("00:00:01:00"); err != nil {
		t.Fatalf("equal (non-backward) timecode should pass: %v", err)
	}
}

func TestCueParsing(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:02,500\nhello\n\n2\n00:00:03,000 --> 00:00:04,000\nworld\n"
	cues, err := ParseSRT(srt)
	if err != nil || len(cues) != 2 {
		t.Fatalf("ParseSRT: %v cues=%d", err, len(cues))
	}
	if cues[0].StartNS != 1e9 || cues[0].EndNS != 2_500_000_000 || cues[0].Text != "hello" {
		t.Errorf("cue0 = %+v", cues[0])
	}
	vtt := "WEBVTT\n\n00:01.000 --> 00:02.500\nhi there\n\nNOTE skip me\n"
	vc, err := ParseWebVTT(vtt)
	if err != nil || len(vc) != 1 || vc[0].Text != "hi there" || vc[0].StartNS != 1e9 {
		t.Fatalf("ParseWebVTT: %v cues=%+v", err, vc)
	}
	if _, err := ParseCue(1, "00:00:02,000", "00:00:02,000", "x"); err == nil {
		t.Error("zero-length cue must fail")
	}
	if _, err := ParseSRT("1\n00:00:01,00 -> 00:00:02,000\nx"); err == nil {
		t.Error("2-digit milliseconds must fail")
	}
}

func TestNanosToTimecodeRoundtrip(t *testing.T) {
	for _, r := range []Rate{Rate24, Rate25, Rate50, Rate2997, Rate2997DF} {
		for _, raw := range []string{"00:00:00:00", "00:01:30:12", "01:02:03:04"} {
			if r.Drop {
				raw = "00:01:00:02"
			}
			m, err := r.Normalize(raw)
			if err != nil {
				t.Fatalf("%s %s: %v", r, raw, err)
			}
			tc, err := r.NanosToTimecode(m.Nanos)
			if err != nil {
				t.Fatalf("inverse %s: %v", r, err)
			}
			back, err := r.Normalize(tc.String())
			if err != nil {
				t.Fatalf("re-normalize %s %s: %v", r, tc, err)
			}
			if back.Frame != m.Frame {
				t.Errorf("%s roundtrip %s: frame %d != %d", r, raw, back.Frame, m.Frame)
			}
		}
	}
}
