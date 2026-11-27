package ts

import (
	"testing"

	"example.com/assetpack/internal/fault"
)

// --- tests ---

func TestParseCleanStream(t *testing.T) {
	var data []byte
	data = append(data, psiPacket(PIDPAT, 0, patSection(0, 1, 0x1000))...)
	data = append(data, psiPacket(0x1000, 0, pmtSection(0, 1, 0x101, 0x102))...)
	for i := 0; i < 10; i++ {
		data = append(data, buildPacket(pktSpec{
			pid: 0x101, cc: uint8(i & 0x0F), pcr: pcrPtr(uint64(90000 * (i + 1))),
		})...)
	}
	s := Parse(data)
	if s.Stats.SyncErrors != 0 || s.Stats.TruncatedBytes != 0 {
		t.Fatalf("unexpected parse faults: %+v", s.Stats)
	}
	if len(s.Packets) != 12 || s.Stats.PCRWraps != 0 {
		t.Fatalf("wrong packet accounting: %d %+v", len(s.Packets), s.Stats)
	}
	last := s.Packets[len(s.Packets)-1]
	if !last.HasPCR || last.PCRNanos != 10000000000 {
		t.Fatalf("bad PCR: %+v", last)
	}
	psi := ParsePSI(s)
	if psi.Current == nil || psi.Current.Version != 0 {
		t.Fatalf("no PMT: %+v", psi.Events)
	}
	a, ok := psi.Current.AudioStream()
	if !ok || a.PID != 0x102 || a.Language != "eng" {
		t.Fatalf("wrong audio stream: %+v", a)
	}
}

func TestParseTruncatedTail(t *testing.T) {
	data := append(buildPacketNoAdapt(pktSpec{pid: 0x100, cc: 0}), 0x47, 0x00, 0x11)
	s := Parse(data)
	f := findFault(s.Faults, fault.KindTruncated)
	if f == nil || f.Offset != PacketSize {
		t.Fatalf("truncation not reported at tail: %+v", s.Faults.Items)
	}
	if !f.Recoverable {
		t.Fatal("trailing truncation should be recoverable")
	}
	if s.Stats.TruncatedBytes != 3 {
		t.Fatalf("truncated byte count %d", s.Stats.TruncatedBytes)
	}
}

func TestParseSyncByteErrorAndResync(t *testing.T) {
	bad := buildPacketNoAdapt(pktSpec{pid: 0x100, cc: 0})
	bad[0] = 0x00
	data := bad
	for i := 0; i < 3; i++ {
		data = append(data, buildPacketNoAdapt(pktSpec{pid: 0x100, cc: uint8(i + 1)})...)
	}
	s := Parse(data)
	f := findFault(s.Faults, fault.KindSyncByte)
	if f == nil || f.Offset != 188 {
		t.Fatalf("sync fault wrong: %+v", s.Faults.Items)
	}
	// Three good packets must still be recognised after resync.
	trusted := 0
	for _, p := range s.Packets {
		if p.Trusted && p.Payload != nil {
			trusted++
		}
	}
	if trusted != 3 {
		t.Fatalf("resync recovered %d trusted packets, want 3", trusted)
	}
}

func TestParseContinuityDuplicateAndJump(t *testing.T) {
	var data []byte
	for _, cc := range []uint8{0, 1, 1, 5} {
		data = append(data, buildPacketNoAdapt(pktSpec{pid: 0x200, cc: cc, payload: []byte{cc}})...)
	}
	s := Parse(data)
	if findFault(s.Faults, fault.KindCCDuplicate) == nil {
		t.Fatal("duplicate CC not reported")
	}
	if findFault(s.Faults, fault.KindCCJump) == nil {
		t.Fatal("CC jump not reported")
	}
	// Duplicate and jumped payloads are withheld from downstream consumers.
	for i, p := range s.Packets {
		if i >= 2 && p.Payload != nil {
			t.Fatalf("packet %d payload should be withheld: %+v", i, p)
		}
	}
}

func TestPSIVersionUpdateRecordsOffset(t *testing.T) {
	var data []byte
	data = append(data, psiPacket(PIDPAT, 0, patSection(0, 1, 0x1000))...)
	data = append(data, psiPacket(0x1000, 0, pmtSection(0, 1, 0x101, 0x102))...)
	for i := 0; i < 3; i++ {
		data = append(data, buildPacketNoAdapt(pktSpec{pid: 0x102, cc: uint8(i)})...)
	}
	updateOff := len(data)
	data = append(data, psiPacket(0x1000, 1, pmtSection(1, 1, 0x101, 0x103))...)
	for i := 0; i < 3; i++ {
		data = append(data, buildPacketNoAdapt(pktSpec{pid: 0x103, cc: uint8(i)})...)
	}
	s := Parse(data)
	psi := ParsePSI(s)
	if len(psi.Events) < 2 {
		t.Fatalf("expected PAT plus two PMT events: %+v", psi.Events)
	}
	ev := psi.Events[len(psi.Events)-1]
	if ev.Table != "PMT" || ev.From != 0 || ev.To != 1 || ev.Offset != int64(updateOff) {
		t.Fatalf("version event wrong: %+v", ev)
	}
	if psi.Current.Version != 1 {
		t.Fatalf("active PMT not advanced: %+v", psi.Current)
	}
	old := psi.ActivePMTAt(int64(updateOff) - 1)
	if old == nil || old.Version != 0 {
		t.Fatalf("historical lookup did not return version 0: %+v", old)
	}
	if a, _ := old.AudioStream(); a.PID != 0x102 {
		t.Fatalf("old map audio pid %d", a.PID)
	}
}

func TestPSIChecksumRejected(t *testing.T) {
	sec := patSection(0, 1, 0x1000)
	sec[len(sec)-1] ^= 0xFF
	data := psiPacket(PIDPAT, 0, sec)
	psi := ParsePSI(Parse(data))
	if findFault(psi.Faults, fault.KindPSIChecksum) == nil {
		t.Fatal("CRC failure not reported")
	}
	if psi.Current != nil || len(psi.PAT) != 0 {
		t.Fatal("corrupt PSI accepted")
	}
}

func TestPSIIgnoresUntrustedPayload(t *testing.T) {
	var data []byte
	// First PAT packet is fine, then a PMT whose continuity jumps.
	data = append(data, psiPacket(PIDPAT, 0, patSection(0, 1, 0x1000))...)
	pmt := psiPacket(0x1000, 0, pmtSection(0, 1, 0x101, 0x102))
	data = append(data, pmt...)
	// Force a CC jump on the PMT PID with a newer version.
	data = append(data, psiPacket(0x1000, 9, pmtSection(1, 1, 0x101, 0x104))...)
	psi := ParsePSI(Parse(data))
	if psi.Current == nil || psi.Current.Version != 0 {
		t.Fatalf("untrusted PMT update was applied: %+v", psi.Events)
	}
	if findFault(psi.Faults, fault.KindCCJump) == nil {
		t.Fatal("jump not surfaced")
	}
}

func TestPSIMultiPacketSection(t *testing.T) {
	// A valid PMT whose section spans two transport packets.
	esInfo := []byte{0x05, 163}
	for i := 0; i < 163; i++ {
		esInfo = append(esInfo, byte(i))
	}
	body := []byte{0xE1, 0x01, 0xF0, 0x00, StreamTypeAAC, 0xE1, 0x02}
	body = append(body, byte(0xF0|len(esInfo)>>8), byte(len(esInfo)))
	body = append(body, esInfo...)
	long := psiSection(TableIDPMT, 1, 0, body)
	if len(long) <= 183 {
		t.Fatalf("test section too short to split: %d", len(long))
	}
	var data []byte
	data = append(data, psiPacket(PIDPAT, 0, patSection(0, 1, 0x1000))...)
	first := append([]byte{0}, long[:183]...)
	data = append(data, buildPacketNoAdapt(pktSpec{pid: 0x1000, cc: 0, pusi: true, payload: first})...)
	data = append(data, buildPacketNoAdapt(pktSpec{pid: 0x1000, cc: 1, payload: long[183:]})...)
	psi := ParsePSI(Parse(data))
	if psi.Current == nil || psi.Current.Version != 0 {
		t.Fatalf("split PSI section not reassembled: %+v", psi.Events)
	}
	if psi.Current.PCRPID != 0x101 {
		t.Fatalf("wrong PCR PID %d", psi.Current.PCRPID)
	}
	if a, ok := psi.Current.AudioStream(); !ok || a.PID != 0x102 {
		t.Fatalf("audio stream lost: %+v", psi.Current.Streams)
	}
	if findFault(psi.Faults, fault.KindPSIChecksum) != nil {
		t.Fatal("valid section rejected")
	}
}
