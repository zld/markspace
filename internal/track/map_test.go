package track

import (
	"errors"
	"testing"

	"liveregress/internal/fault"
)

func threeChan() []Source {
	return []Source{
		{ID: 1, Name: "left", Channels: 3, Kind: "audio", Layer: "a1", Path: "a1/seg0.wav"},
		{ID: 2, Name: "right", Channels: 3, Kind: "audio", Layer: "a2", Path: "a2/seg0.wav"},
	}
}

func TestBuildExactMapping(t *testing.T) {
	plan, err := Build(threeChan(), []Rule{
		{SourceID: 1, Layer: "a1", Role: "a1", ChannelMap: []int{0, 1, 2}, ExpectChannels: 3},
		{SourceID: 2, Layer: "a2", Role: "a2", ChannelMap: []int{2, 1, 0}, ExpectChannels: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Mappings) != 2 {
		t.Fatalf("mappings = %d", len(plan.Mappings))
	}
	if got := plan.Mappings[1].ChannelMap; got[0] != 2 || got[2] != 0 {
		t.Fatalf("channel map = %v", got)
	}
	if roles := plan.Roles(); roles[0] != "a1" || roles[1] != "a2" {
		t.Fatalf("roles = %v", roles)
	}
}

func TestBuildMissingTrack(t *testing.T) {
	_, err := Build(threeChan(), []Rule{{SourceID: 9, Role: "a1", ChannelMap: []int{0}}})
	if !errors.Is(err, fault.ErrMissing) {
		t.Fatalf("want missing, got %v", err)
	}
}

func TestBuildUnmappedTrack(t *testing.T) {
	_, err := Build(threeChan(), []Rule{{SourceID: 1, Layer: "a1", Role: "a1", ChannelMap: []int{0, 1, 2}}})
	if !errors.Is(err, fault.ErrMapping) {
		t.Fatalf("want mapping, got %v", err)
	}
}

func TestBuildDuplicateRole(t *testing.T) {
	_, err := Build(threeChan(), []Rule{
		{SourceID: 1, Layer: "a1", Role: "a1", ChannelMap: []int{0, 1, 2}},
		{SourceID: 2, Layer: "a2", Role: "a1", ChannelMap: []int{0, 1, 2}},
	})
	if !errors.Is(err, fault.ErrMapping) {
		t.Fatalf("want mapping, got %v", err)
	}
}

func TestBuildDuplicateSource(t *testing.T) {
	_, err := Build(threeChan(), []Rule{
		{SourceID: 1, Layer: "a1", Role: "a1", ChannelMap: []int{0, 1, 2}},
		{SourceID: 1, Layer: "a1", Role: "a2", ChannelMap: []int{0, 1, 2}},
	})
	if !errors.Is(err, fault.ErrMapping) {
		t.Fatalf("want mapping, got %v", err)
	}
}

func TestBuildChannelConflict(t *testing.T) {
	cases := []Rule{
		{SourceID: 1, Layer: "a1", Role: "a1", ChannelMap: []int{0, 1, 2}, ExpectChannels: 2},
		{SourceID: 1, Layer: "a1", Role: "a1", ChannelMap: []int{0, 1, 5}},
		{SourceID: 1, Layer: "a1", Role: "a1", ChannelMap: []int{0, 0, 1}},
		{SourceID: 1, Layer: "a1", Role: "a1", ChannelMap: []int{0, 1, 2, 3}},
		{SourceID: 1, Layer: "a1", Role: "a1"},
	}
	for i, r := range cases {
		sources := threeChan()
		// Mark track 2 used so only the rule under test can fail.
		rules := []Rule{r, {SourceID: 2, Layer: "a2", Role: "a2", ChannelMap: []int{0, 1, 2}}}
		if i == 4 {
			rules[0].ChannelMap = nil
		}
		_, err := Build(sources, rules)
		if !errors.Is(err, fault.ErrMapping) {
			t.Fatalf("case %d: want mapping, got %v", i, err)
		}
	}
}

func TestBuildAmbiguousAcrossLayers(t *testing.T) {
	sources := []Source{
		{ID: 1, Layer: "v0", Channels: 1, Kind: "iframes", Path: "v0/i.m3u8"},
		{ID: 1, Layer: "v1", Channels: 1, Kind: "iframes", Path: "v1/i.m3u8"},
	}
	_, err := Build(sources, []Rule{{SourceID: 1, Role: "v0", ChannelMap: []int{0}}})
	if !errors.Is(err, fault.ErrMapping) {
		t.Fatalf("want mapping ambiguity, got %v", err)
	}
	if _, err := Build(sources, []Rule{
		{SourceID: 1, Layer: "v0", Role: "v0", ChannelMap: []int{0}},
		{SourceID: 1, Layer: "v1", Role: "v1", ChannelMap: []int{0}},
	}); err != nil {
		t.Fatalf("layer-qualified ids should resolve: %v", err)
	}
}

func TestParseChannelMap(t *testing.T) {
	got, err := ParseChannelMap("0,2,1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1] != 2 {
		t.Fatalf("map = %v", got)
	}
	for _, bad := range []string{"", "0,0", "a", "0,-1", "0x1"} {
		if _, err := ParseChannelMap(bad); err == nil {
			t.Fatalf("ParseChannelMap(%q) succeeded", bad)
		}
	}
}
