package ts

import (
	"os"
	"path/filepath"
	"testing"

	"tsforge/internal/fixture"
)

// writeFixture generates a clean master into a test temp directory.
func writeFixture(t *testing.T, seed int64) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "clean.ts")
	if _, err := fixture.Generate(path, fixture.DefaultSpec(seed)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return path
}

func TestScannerAlignsAndCountsPackets(t *testing.T) {
	path := writeFixture(t, 1)
	stats, diags, err := ReadAll(path, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("clean master produced %d diagnostics: %+v", len(diags), diags)
	}
	if stats.Packets == 0 || stats.Bytes != stats.Packets*PacketSize {
		t.Fatalf("stats=%+v", stats)
	}
	if stats.Duplicates != 0 || stats.Continuity != 0 || stats.Truncated != 0 {
		t.Fatalf("clean master has transport faults: %+v", stats)
	}
}

func TestScannerDetectsContinuityJump(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.ts")
	if _, err := fixture.Generate(clean, fixture.DefaultSpec(2)); err != nil {
		t.Fatal(err)
	}
	dmg := filepath.Join(dir, "cc.ts")
	dmgs, err := fixture.Mutate(clean, dmg, []string{"cc"}, 2)
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	_, diags, err := ReadAll(dmg, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	// Exactly one packet has its counter advanced by two. Parsing continues
	// afterwards, so the following packet legitimately reports the reverse
	// gap; only the first jump is asserted here.
	var first *Diagnostic
	for i := range diags {
		if diags[i].Kind == DiagContinuityJump {
			first = &diags[i]
			break
		}
	}
	if first == nil {
		t.Fatalf("no continuity diagnostic; mutation=%+v", dmgs)
	}
	if first.PID != fixture.VideoPID {
		t.Errorf("jump on pid=0x%04x, want video", first.PID)
	}
	// The fixture advances one packet's counter, so the expected successor
	// value (prev+1 mod 16) must not be what arrived.
	if first.CC == (first.PrevCC+1)&0x0F {
		t.Errorf("first jump prev=%d cc=%d looks sequential; not a jump", first.PrevCC, first.CC)
	}
	if first.CC == first.PrevCC {
		t.Errorf("first jump reports a duplicate, want a skipped counter")
	}
}

func TestScannerDetectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.ts")
	if _, err := fixture.Generate(clean, fixture.DefaultSpec(3)); err != nil {
		t.Fatal(err)
	}
	dmg := filepath.Join(dir, "dup.ts")
	if _, err := fixture.Mutate(clean, dmg, []string{"dup"}, 3); err != nil {
		t.Fatal(err)
	}
	stats, diags, err := ReadAll(dmg, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if stats.Duplicates == 0 {
		t.Fatalf("duplicate not counted: stats=%+v", stats)
	}
	found := false
	for _, d := range diags {
		if d.Kind == DiagDuplicatePacket {
			found = true
			if d.Offset%PacketSize != 0 {
				t.Errorf("duplicate offset %d not packet-aligned", d.Offset)
			}
		}
	}
	if !found {
		t.Fatalf("no duplicate diagnostic")
	}
}

func TestScannerResynchronizesAfterMisalignment(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.ts")
	if _, err := fixture.Generate(clean, fixture.DefaultSpec(4)); err != nil {
		t.Fatal(err)
	}
	dmg := filepath.Join(dir, "misalign.ts")
	dmgs, err := fixture.Mutate(clean, dmg, []string{"misalign"}, 4)
	if err != nil {
		t.Fatal(err)
	}
	stats, diags, err := ReadAll(dmg, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	found := false
	for _, d := range diags {
		if d.Kind == DiagSyncLost {
			found = true
			if d.Offset != dmgs[0].Offset {
				t.Errorf("sync_lost at %d, mutation at %d", d.Offset, dmgs[0].Offset)
			}
			if d.Skipped != 1 {
				t.Errorf("skipped=%d, want 1 stray byte", d.Skipped)
			}
		}
	}
	if !found {
		t.Fatalf("no sync_lost diagnostic")
	}
	// After resync, the scanner must recover the original packet count.
	if stats.SyncSkipped != 1 {
		t.Errorf("SyncSkipped=%d, want 1", stats.SyncSkipped)
	}
}

func TestScannerReportsTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	clean := filepath.Join(dir, "clean.ts")
	if _, err := fixture.Generate(clean, fixture.DefaultSpec(5)); err != nil {
		t.Fatal(err)
	}
	dmg := filepath.Join(dir, "tail.ts")
	if _, err := fixture.Mutate(clean, dmg, []string{"tail"}, 5); err != nil {
		t.Fatal(err)
	}
	stats, diags, err := ReadAll(dmg, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if stats.Truncated != 1 {
		t.Fatalf("Truncated=%d, want 1", stats.Truncated)
	}
	if len(diags) == 0 || diags[len(diags)-1].Kind != DiagTruncatedPacket {
		t.Fatalf("last diagnostic %+v, want truncated_packet", diags)
	}
}

func TestAdaptationFieldPCRDecoded(t *testing.T) {
	path := writeFixture(t, 6)
	var count int
	_, _, err := ReadAll(path, DefaultConfig(), func(p *Packet) error {
		if p.Adaptation.PCR {
			count++
			if p.PID != fixture.VideoPID {
				t.Errorf("PCR on pid 0x%04x, want video", p.PID)
			}
			if p.Adaptation.PCRExtension > 299 {
				t.Errorf("PCR extension %d out of 27MHz range", p.Adaptation.PCRExtension)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatalf("no PCR packets found")
	}
}

func TestReadPacketMatchesCitedOffset(t *testing.T) {
	path := writeFixture(t, 7)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var first *Packet
	_, _, err = ReadAll(path, DefaultConfig(), func(p *Packet) error {
		if first == nil && p.PID == fixture.VideoPID && p.PayloadStart {
			first = p
		}
		return nil
	})
	if err != nil || first == nil {
		t.Fatalf("no video packet: %v", err)
	}
	back, err := ReadPacket(f, first.Offset)
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if back.PID != first.PID || !back.HasPayload {
		t.Fatalf("re-read mismatch: pid=0x%04x payload=%v", back.PID, back.HasPayload)
	}
	if len(back.Payload) != len(first.Payload) {
		t.Fatalf("payload length %d != %d", len(back.Payload), len(first.Payload))
	}
}
