package fingerprint

import "testing"

func TestFrameAndAudioHashesStableAndDistinct(t *testing.T) {
	pixels := []byte{1, 2, 3, 4, 5, 6}
	h1 := Frame(20_000_000, 2, 1, "rgb24", pixels)
	h2 := Frame(20_000_000, 2, 1, "rgb24", []byte{1, 2, 3, 4, 5, 6})
	if h1 != h2 {
		t.Fatal("identical canonical frames hashed differently")
	}
	if h1 == Frame(20_000_001, 2, 1, "rgb24", pixels) {
		t.Fatal("PTS was not included in frame hash")
	}
	if Pixel(2, 1, pixels) != Pixel(2, 1, append([]byte(nil), pixels...)) {
		t.Fatal("pixel hash not stable")
	}
	a1 := AudioWindow(0, 20_000_000, 48000, []byte{0, 0, 1, 0})
	a2 := AudioWindow(0, 20_000_000, 48000, []byte{0, 0, 1, 0})
	if a1 != a2 {
		t.Fatal("audio window hash not stable")
	}
	if a1 == AudioWindow(0, 20_000_001, 48000, []byte{0, 0, 1, 0}) {
		t.Fatal("audio interval was not canonicalized")
	}
}

func TestRegistryMarksDuplicatesWithoutRemovingLocations(t *testing.T) {
	r := NewRegistry()
	a := Locator{Segment: 0, Kind: "pixel", Index: 3, PTSNS: 30}
	b := Locator{Segment: 1, Kind: "pixel", Index: 9, PTSNS: 90}
	if r.Add("same", a) {
		t.Fatal("first occurrence reported duplicate")
	}
	if !r.Add("same", b) {
		t.Fatal("second occurrence not reported duplicate")
	}
	r.Add("unique", a)
	groups := r.Groups()
	if len(groups) != 1 || groups[0].SHA256 != "same" || len(groups[0].Locations) != 2 {
		t.Fatalf("groups=%+v, want one two-location group", groups)
	}
	if groups[0].Locations[0] != a || groups[0].Locations[1] != b {
		t.Fatalf("locations changed/order wrong: %+v", groups[0].Locations)
	}
}
