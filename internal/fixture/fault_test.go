package fixture

import (
	"bytes"
	"testing"
	"time"

	"streamcheck/internal/clock"
	"streamcheck/internal/ts"
)

func TestInjectPacketDrop(t *testing.T) {
	s := BuildTier(Tier{Name: "900k", BandwidthBPS: 900_000},
		4, 3*time.Second, Options{})
	origPackets := len(s.Data) / ts.PacketSize
	out, err := InjectPacketDrop(s, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(out)/ts.PacketSize != origPackets-5 {
		t.Fatalf("packet count after drop: %d", len(out)/ts.PacketSize)
	}
	// The damaged stream still scans; continuity errors are reported.
	sc := ts.NewScanner(bytes.NewReader(out))
	for sc.Next() != nil {
	}
	if sc.Err() != nil {
		t.Fatal(sc.Err())
	}
	var ccErrs int
	for _, ev := range sc.Events() {
		if ev.Kind == ts.EventCCError {
			ccErrs++
		}
	}
	if ccErrs == 0 {
		t.Fatal("expected continuity errors after a packet drop")
	}
}

func TestInjectPacketDropValidation(t *testing.T) {
	s := BuildTier(Tier{Name: "450k", BandwidthBPS: 450_000},
		2, 3*time.Second, Options{})
	if _, err := InjectPacketDrop(s, 99, 1); err == nil {
		t.Fatal("out-of-range segment must error")
	}
	if _, err := InjectPacketDrop(s, 0, 0); err == nil {
		t.Fatal("zero drop count must error")
	}
}

func TestInjectSegmentSwap(t *testing.T) {
	s := BuildTier(Tier{Name: "1800k", BandwidthBPS: 1_800_000},
		4, 3*time.Second, Options{})
	orig0 := append([]byte(nil), s.Data[s.Segments[0].Start:s.Segments[0].End]...)
	out, err := InjectSegmentSwap(s, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	now1 := out[s.Segments[1].Start:s.Segments[1].End]
	if !bytes.Equal(orig0, now1) {
		t.Fatal("segment 0 content must move to position 1")
	}
}

func TestInjectCorruptTable(t *testing.T) {
	s := BuildTier(Tier{Name: "900k", BandwidthBPS: 900_000},
		2, 3*time.Second, Options{})
	out, err := InjectCorruptTable(s, ts.PIDPAT)
	if err != nil {
		t.Fatal(err)
	}
	// Locate and parse the PAT; its CRC must now fail.
	sc := ts.NewScanner(bytes.NewReader(out))
	var asm ts.SectionAssembler
	var sawCRCError bool
	for p := sc.Next(); p != nil; p = sc.Next() {
		if p.PID != ts.PIDPAT {
			continue
		}
		if sec := asm.Push(*p); sec != nil {
			if _, err := ts.ParsePAT(*sec); err != nil {
				sawCRCError = true
			}
		}
	}
	if !sawCRCError {
		t.Fatal("corrupted PAT must fail CRC validation")
	}
}

func TestInjectCorruptTableUnknownPID(t *testing.T) {
	s := BuildTier(Tier{Name: "450k", BandwidthBPS: 450_000},
		2, 3*time.Second, Options{})
	if _, err := InjectCorruptTable(s, 0x0777); err == nil {
		t.Fatal("unknown table PID must error")
	}
}

func TestInjectPTSWrap(t *testing.T) {
	s, err := InjectPTSWrap(Tier{Name: "450k", BandwidthBPS: 450_000}, 4, 3000)
	if err != nil {
		t.Fatal(err)
	}
	// Re-walk the video PES units and confirm the unwrapper sees a wrap.
	// Each fixture keyframe PES is a self-contained single-packet unit,
	// so the PUSI payloads are parsed directly.
	sc := ts.NewScanner(bytes.NewReader(s.Data))
	var unw clock.Unwrapper
	var sawWrap bool
	for p := sc.Next(); p != nil; p = sc.Next() {
		if p.PID != VideoPID || !p.PUSI {
			continue
		}
		hdr, err := ts.ParsePES(p.Payload())
		if err != nil || !hdr.HasPTS {
			continue
		}
		if _, kind := unw.Feed(hdr.PTS); kind == clock.JumpWrap {
			sawWrap = true
		}
	}
	if !sawWrap {
		t.Fatal("expected at least one PTS wrap")
	}
}

func TestSectionLengthHelper(t *testing.T) {
	sec := patSection(PMTPID)
	if got := sectionLength(sec); got != uint16(len(sec)-3) {
		t.Fatalf("section length: %d vs %d", got, len(sec)-3)
	}
	if len(crcRegion(sec)) != len(sec)-4 {
		t.Fatal("crc region must exclude the trailing CRC")
	}
}
