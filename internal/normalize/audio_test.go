package normalize

import (
	"testing"

	"mediaqc/internal/model"
)

func TestPassthroughAndLayoutCounts(t *testing.T) {
	if LayoutFiveOne.ChannelCount() != 6 || LayoutSevenOne.ChannelCount() != 8 {
		t.Fatal("channel counts wrong")
	}
	p, err := NewAudioPolicy("mat-a", LayoutStereo, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.Plan("a1", LayoutStereo, 48000)
	if err != nil || d.Action != "passthrough" || d.ChannelsDropped != 0 {
		t.Fatalf("stereo->stereo must pass through: %+v err=%v", d, err)
	}
}

func TestDownmixDocumentsEveryChannel(t *testing.T) {
	p, _ := NewAudioPolicy("mat-a", LayoutStereo, 0, 50)
	d, err := p.Plan("a1", LayoutFiveOne, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "downmix" || d.ChannelsDropped != 4 {
		t.Fatalf("downmix decision wrong: %+v", d)
	}
	m, err := DownmixMap(LayoutFiveOne, LayoutStereo)
	if err != nil {
		t.Fatal(err)
	}
	// Every one of the 6 source channels must appear in the fold map.
	covered := map[int]bool{}
	for src, dsts := range m {
		covered[src] = true
		if len(dsts) == 0 {
			t.Fatalf("source channel %d has no destination (silent drop)", src)
		}
	}
	if len(covered) != 6 {
		t.Fatalf("downmix map must cover all 6 channels, got %d", len(covered))
	}
}

func TestUnknownLayoutRejectedNotDefaulted(t *testing.T) {
	p, _ := NewAudioPolicy("mat-a", LayoutStereo, 0, 50)
	_, err := p.Plan("a1", LayoutUnknown, 48000)
	if err == nil || !model.HasCode(err, model.CodeChannelDrop) {
		t.Fatalf("unknown layout must be rejected, got %v", err)
	}
}

func TestResampleRatioAndBudget(t *testing.T) {
	// 48k -> 24k is an integer 1:2 ratio and always fits.
	p, _ := NewAudioPolicy("mat-a", LayoutStereo, 24000, 20)
	d, err := p.Plan("a1", LayoutStereo, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if d.ResampleRatioNum != 24000 || d.ResampleRatioDen != 48000 || d.LatencyBudgetMs != 20 {
		t.Fatalf("resample evidence wrong: %+v", d)
	}
	// Unsupported rate clash with a tiny budget must error.
	strict, _ := NewAudioPolicy("mat-a2", LayoutStereo, 44100, 1)
	if _, err := strict.Plan("a1", LayoutStereo, 48000); err == nil ||
		!model.HasCode(err, model.CodeSampleRateClash) {
		t.Fatalf("unbounded resample must be rejected, got %v", err)
	}
}

func TestDownmixMapValidation(t *testing.T) {
	if _, err := DownmixMap(LayoutMono, LayoutStereo); err == nil {
		t.Fatal("upmix passed to DownmixMap must error")
	}
	if _, err := DownmixMap(LayoutSevenOne, LayoutMono); err == nil {
		t.Fatal("undocumented downmix path must error, not silently drop")
	}
	m, err := DownmixMap(LayoutSevenOne, LayoutFiveOne)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 6 {
		t.Fatalf("7.1->5.1 map should retain 6 source entries, got %d", len(m))
	}
}

func TestPolicyValidation(t *testing.T) {
	if _, err := NewAudioPolicy("m", LayoutUnknown, 0, 10); err == nil {
		t.Fatal("unknown target layout must be rejected")
	}
	if _, err := NewAudioPolicy("m", LayoutStereo, 0, 0); err == nil {
		t.Fatal("non-positive latency budget must be rejected")
	}
}
