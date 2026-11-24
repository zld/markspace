package ts

import (
	"bytes"
	"errors"
	"testing"

	"mediad/internal/clock"
)

// buildStream assembles PAT, PMT and a video PID carrying PCR plus payloads.
func buildStream(t *testing.T, videoPID, pmtPID uint16, count int) []byte {
	t.Helper()
	var out []byte
	out = append(out, tablePacket(PIDPAT, true, 0, patSection(1, pmtPID, 1))...)
	out = append(out, tablePacket(pmtPID, true, 0, pmtSection(1, videoPID, videoPID, 0x1b, 1))...)
	for i := 0; i < count; i++ {
		// PCR every packet keeps the timeline anchored; CC increments.
		pkt := pcrPacket(videoPID, uint8(i%16), int64(i)*ticksPerPacket)
		pkt[3] = 0x30 | uint8(i%16) // adaptation + payload
		pkt[12] = byte(0xa0 + i)    // payload marker after 8-byte adaptation
		out = append(out, pkt...)
	}
	return out
}

func TestDemuxHappyPath(t *testing.T) {
	stream := buildStream(t, 0x0101, 0x1000, 8)
	res, err := Demux(bytes.NewReader(stream), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("clean stream must have zero errors, got %+v", res.Errors)
	}
	if res.Duplicates != 0 {
		t.Fatalf("duplicates = %d, want 0", res.Duplicates)
	}
	if len(res.Events) != 8 {
		t.Fatalf("events = %d, want 8", len(res.Events))
	}
	// Every event shares the monotonic timeline and moves forward.
	for i, ev := range res.Events {
		want := int64(i) * ticksPerPacket
		if ev.Start != want || ev.End != want+ticksPerPacket {
			t.Fatalf("event %d time = [%d,%d], want [%d,%d]", i, ev.Start, ev.End, want, want+ticksPerPacket)
		}
		if ev.TrackID != "pid-0101" {
			t.Fatalf("track id = %q", ev.TrackID)
		}
	}
	if res.Map.EffectivePIDs[0] != 0x0101 {
		t.Fatalf("effective PID = %v", res.Map.EffectivePIDs)
	}
}

func TestDemuxDeduplicatesAndReportsJumps(t *testing.T) {
	stream := buildStream(t, 0x0101, 0x1000, 4)
	// Insert a duplicate (same CC as last) and a jump packet.
	dup := pcrPacket(0x0101, 3, 3*ticksPerPacket)
	jump := pcrPacket(0x0101, 9, 4*ticksPerPacket)
	stream = append(stream, dup...)
	stream = append(stream, jump...)
	res, err := Demux(bytes.NewReader(stream), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", res.Duplicates)
	}
	var found bool
	for _, e := range res.Errors {
		if e.Kind == clock.KindContinuity {
			found = true
		}
	}
	if !found {
		t.Fatalf("continuity jump must be reported: %+v", res.Errors)
	}
}

func TestDemuxTruncationIsFatalAndKeepsDiagnostics(t *testing.T) {
	stream := buildStream(t, 0x0101, 0x1000, 3)
	stream = append(stream, 0x47, 0xab, 0xcd) // partial packet
	res, err := Demux(bytes.NewReader(stream), ScanOptions{})
	if err == nil {
		t.Fatal("truncated input must return an error")
	}
	var re clock.RangeError
	if !errors.As(err, &re) || re.Kind != clock.KindTruncated {
		t.Fatalf("want truncated RangeError, got %v", err)
	}
	// Diagnostics retained and the truncation offset is exact.
	wantOffset := int64(2 /* PAT+PMT */ +3) * PacketSize
	if re.Start != wantOffset {
		t.Fatalf("truncation offset = %d, want %d", re.Start, wantOffset)
	}
	if len(res.Errors) == 0 || res.Errors[len(res.Errors)-1].Kind != clock.KindTruncated {
		t.Fatal("result must retain the truncation diagnostic")
	}
}

func TestDemuxSyncErrorRecovered(t *testing.T) {
	stream := buildStream(t, 0x0101, 0x1000, 2)
	// Inject garbage before a recoverable packet.
	garbage := []byte{0x00, 0x01, 0x02}
	res, err := Demux(bytes.NewReader(append(garbage, stream...)), ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var hasSync bool
	for _, e := range res.Errors {
		if e.Kind == clock.KindSync && e.Start == 0 && e.End == 3 {
			hasSync = true
		}
	}
	if !hasSync {
		t.Fatalf("resync must report a sync error over the garbage range: %+v", res.Errors)
	}
}
