package events

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"replay/internal/clock"
)

func packetB64(t *testing.T, seq uint16, ts uint32) string {
	t.Helper()
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96,
			SequenceNumber: seq, Timestamp: ts, SSRC: 1,
		},
		Payload: []byte{0xAA},
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("marshal rtp: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestUnwrapMonotonicAcrossRollover(t *testing.T) {
	s, err := NewStream(90000, Anchor{RtpTick: 0xFFFFFF00, MediaTick: 0})
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	// Two packets straddling the 32-bit boundary with a positive step.
	first, err := s.Ingest(mustDecode(t, packetB64(t, 1, 0xFFFFFF00)))
	if err != nil {
		t.Fatalf("ingest first: %v", err)
	}
	second, err := s.Ingest(mustDecode(t, packetB64(t, 2, 0x00000100)))
	if err != nil {
		t.Fatalf("ingest second: %v", err)
	}
	if second.Ticks <= first.Ticks {
		t.Fatalf("unwrapped time must advance: %d -> %d", first.Ticks, second.Ticks)
	}
	if s.Wraps != 1 {
		t.Fatalf("wraps=%d want 1", s.Wraps)
	}
}

func TestBadPacketCountedAndIsolated(t *testing.T) {
	s, _ := NewStream(90000, Anchor{RtpTick: 1000, MediaTick: 0})
	if _, err := s.Ingest([]byte{0x80, 0x60}); err == nil {
		t.Fatal("expected malformed packet error")
	}
	if s.Bad != 1 {
		t.Fatalf("bad=%d want 1", s.Bad)
	}
	// A subsequent valid packet must still parse on the same stream.
	mt, err := s.Ingest(mustDecode(t, packetB64(t, 2, 2000)))
	if err != nil {
		t.Fatalf("valid packet after bad one failed: %v", err)
	}
	if mt.Ticks == 0 {
		t.Fatal("expected a mapped media time after recovery")
	}
}

func TestLoadOrdersDedupesAndConflicts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feed.jsonl")
	lines := []string{
		`{"id":"a","confidence":0.9,"duration_s":1,"content":"x","packets":["` + packetB64(t, 1, 1000) + `"]}`,
		`{"id":"a","confidence":0.9,"duration_s":1,"content":"x","packets":["` + packetB64(t, 2, 1000) + `"]}`,
	}
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	feed, err := Load(path, 90000, Anchor{RtpTick: 1000, MediaTick: 0}, 0.6)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(feed.Events) != 2 {
		t.Fatalf("want 2 records, got %d", len(feed.Events))
	}
	if feed.Events[1].Decision != DecisionDuplicate {
		t.Fatalf("second record decision=%s want duplicate", feed.Events[1].Decision)
	}
}

func TestLoadRejectsConflictingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conflict.jsonl")
	lines := []string{
		`{"id":"a","confidence":0.9,"content":"x","packets":["` + packetB64(t, 1, 1000) + `"]}`,
		`{"id":"a","confidence":0.9,"content":"DIFFERENT","packets":["` + packetB64(t, 2, 1000) + `"]}`,
	}
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, 90000, Anchor{RtpTick: 1000, MediaTick: 0}, 0.6)
	ce, ok := clock.AsClockError(err)
	if !ok || ce.Kind != clock.KindConflict {
		t.Fatalf("want conflict error, got %v", err)
	}
}

func TestLowConfidenceSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "low.jsonl")
	line := `{"id":"z","confidence":0.3,"content":"c","packets":["` + packetB64(t, 1, 1000) + `"]}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	feed, err := Load(path, 90000, Anchor{RtpTick: 1000, MediaTick: 0}, 0.6)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if feed.Events[0].Decision != DecisionSkipConfidence {
		t.Fatalf("decision=%s want skip_confidence", feed.Events[0].Decision)
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return raw
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}
