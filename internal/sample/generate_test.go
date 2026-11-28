package sample

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"rtp-rebuild/internal/clock"
	"rtp-rebuild/internal/events"
	"rtp-rebuild/internal/ingest"
	"rtp-rebuild/internal/rtcp"
	"rtp-rebuild/internal/rtp"
)

func readAll(t *testing.T, data []byte) (rtpPackets, rtcpRecords, eventLines int, firstRTCPErr error) {
	t.Helper()
	s, err := ingest.NewStream(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	for {
		rec, err := s.ReadRecord()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if firstRTCPErr == nil {
				firstRTCPErr = err
			}
			break
		}
		switch rec.Kind {
		case ingest.KindRTP:
			rtpPackets++
		case ingest.KindRTCP:
			rtcpRecords++
			if _, err := rtcp.ParseCompound(rec.Data, rec.PayloadOffset()); err != nil && firstRTCPErr == nil {
				firstRTCPErr = err
			}
		case ingest.KindEvent:
			eventLines++
		}
	}
	return
}

func TestNormalStreamShape(t *testing.T) {
	data, err := Build(NormalSpec(), 7)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	n, rc, ev, _ := readAll(t, data)
	if n != DefaultPackets {
		t.Fatalf("rtp records = %d, want %d", n, DefaultPackets)
	}
	if rc == 0 || ev == 0 {
		t.Fatalf("rtcp=%d events=%d", rc, ev)
	}
}

func TestDeterministicForSeed(t *testing.T) {
	a, _ := Build(NormalSpec(), 7)
	b, _ := Build(NormalSpec(), 7)
	if !bytes.Equal(a, b) {
		t.Fatal("same seed must produce identical bytes")
	}
	c, _ := Build(NormalSpec(), 8)
	if bytes.Equal(a, c) {
		t.Fatal("different seeds should differ (ordering is seeded)")
	}
}

func TestFaultStreamHasDamage(t *testing.T) {
	data, err := Build(FaultSpec(), 8)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	s, err := ingest.NewStream(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r := rtp.NewReorderer(rtp.DefaultWindow)
	var truncOff int64 = -1
	for {
		rec, err := s.ReadRecord()
		if err != nil {
			break
		}
		switch rec.Kind {
		case ingest.KindRTP:
			if err := r.Push(rec.Data, rec.Offset); err != nil {
				t.Fatalf("push: %v", err)
			}
		case ingest.KindRTCP:
			if _, err := rtcp.ParseCompound(rec.Data, rec.PayloadOffset()); err != nil {
				truncOff = rec.PayloadOffset()
			}
		}
	}
	res := r.Finish()
	if res.Duplicates == 0 {
		t.Fatal("fault stream should contain duplicates")
	}
	if res.LossesTotal() == 0 {
		t.Fatal("fault stream should contain loss")
	}
	if res.TimestampWraps == 0 {
		t.Fatal("fault stream should cross the 32 bit timestamp wrap")
	}
	if truncOff < 0 {
		t.Fatal("fault stream should contain a truncated RTCP record")
	}
}

func TestFaultStreamGap(t *testing.T) {
	data, _ := Build(FaultSpec(), 8)
	s, _ := ingest.NewStream(bytes.NewReader(data))
	r := rtp.NewReorderer(rtp.DefaultWindow)
	for {
		rec, err := s.ReadRecord()
		if err != nil {
			break
		}
		if rec.Kind == ingest.KindRTP {
			_ = r.Push(rec.Data, rec.Offset)
		}
	}
	res := r.Finish()
	// The injected 60000 tick gap must appear as one interval above 18000.
	var ts []clock.Tick
	for _, p := range res.Packets {
		if !p.TSSuspect {
			ts = append(ts, p.TS)
		}
	}
	gaps := 0
	irregular := 0
	for i := 1; i < len(ts); i++ {
		d := ts[i] - ts[i-1]
		if d > clock.GapThresholdTicks {
			gaps++
		}
		if d != clock.Tick(PacketInterval) {
			irregular++
		}
	}
	if gaps < 1 {
		t.Fatalf("gap intervals = %d, want >= 1", gaps)
	}
	// Only the injected stall plus the two-packet loss may disturb spacing.
	if irregular > 2 {
		t.Fatalf("irregular deltas = %d, want <= 2", irregular)
	}
}

func TestEventLinesParse(t *testing.T) {
	data, _ := Build(FaultSpec(), 8)
	s, _ := ingest.NewStream(bytes.NewReader(data))
	var lines []byte
	for {
		rec, err := s.ReadRecord()
		if err != nil {
			break
		}
		if rec.Kind == ingest.KindEvent {
			lines = append(lines, rec.Data...)
			lines = append(lines, '\n')
		}
	}
	log := events.Parse(lines)
	if len(log.Events) == 0 {
		t.Fatal("no events generated")
	}
	if log.Unalignable == 0 {
		t.Fatal("fault spec should include an unalignable event")
	}
}

func TestWriteToDisk(t *testing.T) {
	dir := t.TempDir()
	st, err := Write(dir, NormalSpec(), 7)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if st.Path == "" {
		t.Fatal("path not set")
	}
	if st.TruncatedRTCPOffset != -1 {
		t.Fatalf("normal stream truncated offset = %d", st.TruncatedRTCPOffset)
	}
}
