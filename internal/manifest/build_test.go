package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"regress/internal/media"
)

func sampleVariants() []Variant {
	return []Variant{
		{
			Name: "high", Bandwidth: 2000000, Codec: "avc1", Subtitles: "sub",
			Segments: []Segment{
				{URI: "high-0.ts", Duration: media.NewRat(10, 1)},
				{URI: "high-1.ts", Duration: media.NewRat(10, 1)},
			},
		},
		{
			Name: "low", Bandwidth: 500000, Codec: "avc1", Subtitles: "sub",
			Segments: []Segment{
				{URI: "low-0.ts", Duration: media.NewRat(10, 1)},
			},
		},
	}
}

func TestBuildDeterministicAndSorted(t *testing.T) {
	p1 := Build(sampleVariants())
	// Reverse input order; output must be identical because of stable sorting.
	reversed := []Variant{sampleVariants()[1], sampleVariants()[0]}
	p2 := Build(reversed)

	if p1.MasterText != p2.MasterText {
		t.Fatalf("manifest not order-independent:\n%s\n---\n%s", p1.MasterText, p2.MasterText)
	}
	if p1.SHA256 != p2.SHA256 {
		t.Fatal("digest differs across runs with equal content")
	}
	// low bandwidth must be listed first.
	lowIdx := strings.Index(p1.MasterText, "low.m3u8")
	highIdx := strings.Index(p1.MasterText, "high.m3u8")
	if lowIdx < 0 || highIdx < 0 || lowIdx > highIdx {
		t.Fatalf("variants not bandwidth-sorted:\n%s", p1.MasterText)
	}
}

func TestSHA256Is64HexAndMatches(t *testing.T) {
	p := Build(sampleVariants())
	matched, err := regexp.MatchString(`^[0-9a-f]{64}$`, p.SHA256)
	if err != nil || !matched {
		t.Fatalf("digest %q is not 64 lowercase hex chars", p.SHA256)
	}
	sum := sha256.Sum256([]byte(p.MasterText))
	if p.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("digest does not match master playlist bytes")
	}
}

func TestRepeatedBuildsByteIdentical(t *testing.T) {
	p1 := Build(sampleVariants())
	p2 := Build(sampleVariants())
	if p1.MasterText != p2.MasterText {
		t.Fatal("repeated builds diverged")
	}
	if p1.MediaText["high"] != p2.MediaText["high"] {
		t.Fatal("media playlist diverged across builds")
	}
}

func TestMediaPlaylistAndDuration(t *testing.T) {
	p := Build(sampleVariants())
	high := p.MediaText["high"]
	if !strings.Contains(high, "#EXTINF:10.000,") ||
		!strings.HasPrefix(high, "#EXTM3U\n") ||
		!strings.HasSuffix(high, "#EXT-X-ENDLIST\n") {
		t.Fatalf("media playlist malformed:\n%s", high)
	}
	if p.SegmentCount != 3 {
		t.Fatalf("segment count=%d, want 3", p.SegmentCount)
	}
	// Fractional exact duration renders with fixed three decimals.
	frac := Build([]Variant{{
		Name: "x", Bandwidth: 1, Codec: "c",
		Segments: []Segment{{URI: "x.ts", Duration: media.NewRat(1, 3)}},
	}})
	if !strings.Contains(frac.MediaText["x"], "#EXTINF:0.333,") {
		t.Fatalf("fractional duration rendered wrong:\n%s", frac.MediaText["x"])
	}
}

func TestNoSubtitleGroupWhenAbsent(t *testing.T) {
	v := []Variant{{Name: "solo", Bandwidth: 100, Codec: "mp4a",
		Segments: []Segment{{URI: "a.ts", Duration: media.NewInt(2)}}}}
	p := Build(v)
	if strings.Contains(p.MasterText, "EXT-X-MEDIA") {
		t.Fatalf("subtitle group should be absent:\n%s", p.MasterText)
	}
	if strings.Contains(p.MasterText, "SUBTITLES=") {
		t.Fatal("SUBTITLES attribute should be absent")
	}
}
