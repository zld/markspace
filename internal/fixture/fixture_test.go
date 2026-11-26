package fixture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediamerge/internal/mp4idx"
	"mediamerge/internal/rtpclock"
	"mediamerge/internal/subtitle"
)

func TestGenerateNormal(t *testing.T) {
	dir := t.TempDir()
	b, err := Generate(dir, CaseNormal)
	if err != nil {
		t.Fatal(err)
	}
	srt, err := subtitle.ParseSRT(b.SRTPath, "v1", mustOpen(t, b.SRTPath))
	if err != nil || len(srt.Cues) != 8 {
		t.Fatalf("normal SRT: %v cues=%d", err, len(srt.Cues))
	}
	vtt, err := subtitle.ParseVTT(b.VTTPath, "v2", mustOpen(t, b.VTTPath))
	if err != nil || len(vtt.Cues) != 8 {
		t.Fatalf("normal VTT: %v cues=%d", err, len(vtt.Cues))
	}
	idx, err := mp4idx.NewReader(b.MP4Path, seekFile(t, b.MP4Path)).Read()
	if err != nil {
		t.Fatalf("normal mp4: %v", err)
	}
	if len(idx.Samples) != 8 || idx.SyncCount != 8 {
		t.Fatalf("normal index samples=%d sync=%d", len(idx.Samples), idx.SyncCount)
	}
	evs, err := ReadRTPStream(b.RTPPath, mustOpen(t, b.RTPPath), 90000)
	if err != nil || len(evs) != 8 {
		t.Fatalf("rtp: %v events=%d", err, len(evs))
	}
	for _, ev := range evs {
		if ev.Kind != rtpclock.EventOK {
			t.Fatalf("normal RTP must have no anomalies, got %s", ev.Kind)
		}
	}
}

func TestGenerateDamaged(t *testing.T) {
	dir := t.TempDir()
	b, err := Generate(dir, CaseDamaged)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Damaged) != 3 {
		t.Fatalf("damaged bundle must declare three faults: %v", b.Damaged)
	}
	// Lenient SRT: one rejected (DF), seven accepted.
	tr, rej, err := subtitle.ParseSRTWithOptions(b.SRTPath, "v1", mustOpen(t, b.SRTPath),
		subtitle.Options{Lenient: true})
	if err != nil {
		t.Fatalf("lenient parse: %v", err)
	}
	if len(tr.Cues) != 7 || len(rej) != 1 {
		t.Fatalf("want 7 cues and 1 rejection, got cues=%d rej=%d", len(tr.Cues), len(rej))
	}
	// Incomplete sync index: no stss, rebuild marks all samples sync.
	idx, err := mp4idx.NewReader(b.MP4Path, seekFile(t, b.MP4Path)).Read()
	if err != nil {
		t.Fatalf("damaged mp4 must still parse: %v", err)
	}
	if len(idx.Samples) != 8 || idx.SyncCount != 8 {
		t.Fatalf("rebuilt index must keep 8/8 keyframes: samples=%d sync=%d",
			len(idx.Samples), idx.SyncCount)
	}
}

func TestGenerateConflict(t *testing.T) {
	dir := t.TempDir()
	b, err := Generate(dir, CaseConflict)
	if err != nil {
		t.Fatal(err)
	}
	srt, _ := subtitle.ParseSRT(b.SRTPath, "v1", mustOpen(t, b.SRTPath))
	vtt, _ := subtitle.ParseVTT(b.VTTPath, "v2", mustOpen(t, b.VTTPath))
	if srt.Cues[3].Text == vtt.Cues[3].Text {
		t.Fatal("conflict fixture must differ on cue 4 text")
	}
}

func TestDeterministicFiles(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	b1, _ := Generate(d1, CaseNormal)
	_, _ = Generate(d2, CaseNormal)
	for _, p := range []string{b1.MP4Path, b1.RTPPath, b1.MappingPath} {
		rel, _ := filepath.Rel(d1, p)
		a, err := os.ReadFile(filepath.Join(d1, rel))
		if err != nil {
			t.Fatal(err)
		}
		bBytes, err := os.ReadFile(filepath.Join(d2, rel))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(bBytes) {
			t.Fatalf("generated %s must be byte-deterministic", rel)
		}
	}
	if !strings.Contains(string(mustRead(t, b1.MappingPath)), "timeline-en") {
		t.Fatal("mapping doc must name the target timeline track")
	}
}
