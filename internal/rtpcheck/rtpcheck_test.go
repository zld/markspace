package rtpcheck

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
)

func writeDump(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseDumpPinsPackets(t *testing.T) {
	path := writeDump(t, "# ssrc seq ts rate\n0x2222 0 0 48000\n")
	// Hex is not accepted; the dump is decimal-only so it cannot be ambiguous.
	if _, _, err := ParseDump(path); err == nil {
		t.Fatal("expected hex rejection")
	}
	path = writeDump(t, "# ssrc seq ts rate\n8738 0 0 48000\n8738 1 960 48000\n")
	recs, ssrcTrack, err := ParseDump(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d", len(recs))
	}
	if len(ssrcTrack) != 1 {
		t.Fatalf("streams = %d", len(ssrcTrack))
	}
	if recs[1].Seq != 1 || recs[1].TS != 960 {
		t.Fatalf("record = %+v", recs[1])
	}
	// The packet must round-trip through pion/rtp, not a private struct.
	if recs[0].Pion.SSRC != 8738 || recs[0].Pion.SequenceNumber != 0 {
		t.Fatalf("pion decode = %+v", recs[0].Pion)
	}
}

func TestParseDumpRejectsMalformedLines(t *testing.T) {
	cases := map[string]string{
		"too few fields":   "8738 0 0\n",
		"non-numeric":      "abc 0 0 48000\n",
		"zero clock rate":  "8738 0 0 0\n",
		"seq over 16 bits": "8738 70000 0 48000\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := ParseDump(writeDump(t, body))
			if err == nil {
				t.Fatal("expected error")
			}
			if timeline.ExitCodeOf(err) != timeline.ExitBadInput {
				t.Fatalf("exit = %d", timeline.ExitCodeOf(err))
			}
		})
	}
}

func TestSequenceGapDetected(t *testing.T) {
	path := writeDump(t, "1 0 0 48000\n1 2 1920 48000\n")
	recs, _, err := ParseDump(path)
	if err != nil {
		t.Fatal(err)
	}
	rep := Validate(recs, nil)
	if rep.OK() {
		t.Fatal("gap not detected")
	}
	if rep.Problems[0].Category != timeline.CatMissingElement {
		t.Fatalf("category = %s", rep.Problems[0].Category)
	}
}

func TestDuplicateAndBackwardsSequence(t *testing.T) {
	recs, _, err := ParseDump(writeDump(t, "1 5 0 48000\n1 5 960 48000\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rep := Validate(recs, nil); rep.Problems[0].Category != timeline.CatDuplicate {
		t.Fatalf("problems = %+v", rep.Problems)
	}
	recs, _, err = ParseDump(writeDump(t, "1 5 1000 48000\n1 4 2000 48000\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rep := Validate(recs, nil); rep.Problems[0].Category != timeline.CatOrder {
		t.Fatalf("problems = %+v", rep.Problems)
	}
}

func TestTimestampAlignmentAgainstFragments(t *testing.T) {
	recs, _, err := ParseDump(writeDump(t, "1 0 0 48000\n"))
	if err != nil {
		t.Fatal(err)
	}
	a := &media.Asset{Fragments: []media.Fragment{{TrackNumber: 1, Start: 0}}}
	if rep := Validate(recs, a); !rep.OK() {
		t.Fatalf("aligned packets rejected: %+v", rep.Problems)
	}
	// One RTP tick at 48 kHz is ~20.8 microseconds, so 48 ticks is ~1 ms.
	recs, _, err = ParseDump(writeDump(t, "1 0 49 48000\n"))
	if err != nil {
		t.Fatal(err)
	}
	rep := Validate(recs, a)
	if rep.OK() {
		t.Fatal("misaligned start accepted")
	}
	if rep.MaxSkew <= timeline.MaxTolerance {
		t.Fatalf("skew = %d", rep.MaxSkew)
	}
	if rep.Problems[0].Category != timeline.CatFragmentConflict {
		t.Fatalf("category = %s", rep.Problems[0].Category)
	}
}

func TestSSRCOrderingIsStable(t *testing.T) {
	recs, mapping, err := ParseDump(writeDump(t, "9 0 0 90000\n3 0 0 48000\n9 1 1 90000\n"))
	if err != nil {
		t.Fatal(err)
	}
	if mapping[9] != 1 || mapping[3] != 2 {
		t.Fatalf("mapping = %v; first-seen SSRC must be track 1", mapping)
	}
	if recs[2].Track != 1 {
		t.Fatalf("second packet track = %d", recs[2].Track)
	}
}
