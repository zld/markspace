package audio

import (
	"math"
	"strings"
	"testing"
)

func TestLayoutChannels(t *testing.T) {
	cases := map[Layout]int{Mono: 1, Stereo: 2, Five1: 6}
	for l, want := range cases {
		got, err := l.Channels()
		if err != nil || got != want {
			t.Fatalf("%s: %d %v", l, got, err)
		}
	}
	if _, err := Layout("quad").Channels(); err == nil {
		t.Fatal("unknown layout must error")
	}
}

func TestParseLayoutAliases(t *testing.T) {
	for _, s := range []string{"mono", "stereo", "5.1", "5_1", "five1"} {
		if _, err := ParseLayout(s); err != nil {
			t.Fatalf("%q: %v", s, err)
		}
	}
	if _, err := ParseLayout("surround"); err == nil {
		t.Fatal("unknown must error")
	}
}

func TestChannelMapMonoToStereo(t *testing.T) {
	m, err := BuildChannelMap(Mono, Stereo)
	if err != nil {
		t.Fatal(err)
	}
	in := []float64{0.5, 0.25, -0.3}
	out, err := m.Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	for f := 0; f < 3; f++ {
		if out[f*2] != in[f] || out[f*2+1] != in[f] {
			t.Fatalf("frame %d: %v", f, out[f*2:f*2+2])
		}
	}
	if len(m.Dropped) != 0 {
		t.Fatalf("dropped %v", m.Dropped)
	}
}

func TestChannelMapMonoToFive1ZeroFills(t *testing.T) {
	m, err := BuildChannelMap(Mono, Five1)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := m.Apply([]float64{1, 2})
	if len(out) != 2*6 {
		t.Fatalf("got %d", len(out))
	}
	for f := 0; f < 2; f++ {
		base := f * 6
		wantCentre := []float64{0, 0, float64(f + 1), 0, 0, 0}
		for c := 0; c < 6; c++ {
			if out[base+c] != wantCentre[c] {
				t.Fatalf("frame %d chan %d = %v, want %v", f, c, out[base+c], wantCentre[c])
			}
		}
	}
	if len(m.Filled) != 5 {
		t.Fatalf("filled %v", m.Filled)
	}
}

func TestChannelMapStereoToFive1ZeroFills(t *testing.T) {
	m, err := BuildChannelMap(Stereo, Five1)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := m.Apply([]float64{0.7, -0.2})
	want := []float64{0.7, -0.2, 0, 0, 0, 0}
	for c := range want {
		if out[c] != want[c] {
			t.Fatalf("chan %d = %v want %v", c, out[c], want[c])
		}
	}
}

func TestChannelMapFive1ToStereo(t *testing.T) {
	m, err := BuildChannelMap(Five1, Stereo)
	if err != nil {
		t.Fatal(err)
	}
	// FL=1, FR=0, C=0, SL=0, SR=0, LFE=0 -> L=1, R=0.
	in := []float64{1, 0, 0, 0, 0, 0}
	out, _ := m.Apply(in)
	if out[0] != 1 || out[1] != 0 {
		t.Fatalf("got %v", out)
	}
	// Centre contributes half to each.
	in = []float64{0, 0, 1, 0, 0, 0}
	out, _ = m.Apply(in)
	if out[0] != 0.5 || out[1] != 0.5 {
		t.Fatalf("centre: got %v", out)
	}
	if len(m.Dropped) != 0 {
		t.Fatalf("five1->stereo dropped %v", m.Dropped)
	}
}

func TestChannelMapFive1ToMonoNoDrop(t *testing.T) {
	m, err := BuildChannelMap(Five1, Mono)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Dropped) != 0 {
		t.Fatalf("dropped %v", m.Dropped)
	}
}

func TestChannelMapIdentity(t *testing.T) {
	for _, l := range Layouts {
		m, err := BuildChannelMap(l, l)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := l.Channels()
		in := make([]float64, n*4)
		for i := range in {
			in[i] = float64(i) * 0.01
		}
		out, _ := m.Apply(in)
		for i := range in {
			if math.Abs(out[i]-in[i]) > 1e-12 {
				t.Fatalf("%s identity differs at %d", l, i)
			}
		}
	}
}

func TestChannelMapNoSilentDestinationFromRealInput(t *testing.T) {
	// Downmixes must not zero-fill any destination when the source has content.
	for _, c := range []struct{ from, to Layout }{
		{Five1, Stereo}, {Five1, Mono}, {Stereo, Mono},
	} {
		m, err := BuildChannelMap(c.from, c.to)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Filled) != 0 {
			t.Fatalf("%s->%s filled %v", c.from, c.to, m.Filled)
		}
	}
}

func TestChannelMapKnownSourceNotDropped(t *testing.T) {
	// Every source channel of a downmix must reach some destination.
	for _, c := range []struct{ from, to Layout }{
		{Five1, Stereo}, {Five1, Mono}, {Stereo, Mono},
	} {
		m, err := BuildChannelMap(c.from, c.to)
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Dropped) != 0 {
			t.Fatalf("%s->%s drops %v", c.from, c.to, m.Dropped)
		}
	}
}

func TestChannelMapBadBlock(t *testing.T) {
	m, _ := BuildChannelMap(Stereo, Mono)
	if _, err := m.Apply([]float64{1, 2, 3}); err != ErrBadBlock {
		t.Fatalf("got %v", err)
	}
}

func TestChannelMapRejectedCombo(t *testing.T) {
	// There is no 4-channel layout, so this can only fail on the layout itself.
	if _, err := BuildChannelMap(Layout("quad"), Stereo); err == nil || !strings.Contains(err.Error(), "unknown layout") {
		t.Fatalf("got %v", err)
	}
}
