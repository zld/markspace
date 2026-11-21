package fixture

import (
	"bytes"
	"testing"
	"time"

	"streamcheck/internal/ts"
)

func TestBuildTierStructure(t *testing.T) {
	stream := BuildTier(Tier{Name: "900k", BandwidthBPS: 900_000},
		12, 3*time.Second, Options{})
	if len(stream.Data) == 0 || len(stream.Data)%ts.PacketSize != 0 {
		t.Fatalf("data must be whole TS packets, len=%d", len(stream.Data))
	}
	if len(stream.Segments) != 12 {
		t.Fatalf("segments: %d", len(stream.Segments))
	}
	// Ranges must be packet-aligned, ordered and contiguous; the stream
	// begins with a PSI preamble so the first range starts above zero.
	prevEnd := stream.Segments[0].Start
	for i, r := range stream.Segments {
		if r.Sequence != i || r.Start != prevEnd || r.End <= r.Start {
			t.Fatalf("bad range %d: %+v", i, r)
		}
		if r.Start%ts.PacketSize != 0 || r.End%ts.PacketSize != 0 {
			t.Fatalf("range not packet-aligned: %+v", r)
		}
		prevEnd = r.End
	}
	if prevEnd != len(stream.Data) {
		t.Fatalf("ranges do not cover stream tail: %d vs %d", prevEnd, len(stream.Data))
	}
}

func TestBuildTierParsesAndDetectsVideo(t *testing.T) {
	stream := BuildTier(Tier{Name: "450k", BandwidthBPS: 450_000},
		4, 3*time.Second, Options{})
	sc := ts.NewScanner(bytes.NewReader(stream.Data))
	var sawPAT, sawPMT, sawVideo bool
	var assembler ts.SectionAssembler
	pat := map[uint16]bool{ts.PIDPAT: true, PMTPID: true}
	for p := sc.Next(); p != nil; p = sc.Next() {
		switch {
		case pat[p.PID]:
			if sec := assembler.Push(*p); sec != nil {
				if sec.TableID == 0x00 {
					sawPAT = true
				}
				if sec.TableID == 0x02 {
					pmt, err := ts.ParsePMT(*sec)
					if err != nil {
						t.Fatal(err)
					}
					if pmt.VideoPID() != VideoPID {
						t.Fatalf("video pid: %#x", pmt.VideoPID())
					}
					sawPMT = true
				}
			}
		case p.PID == VideoPID:
			sawVideo = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawPAT || !sawPMT || !sawVideo {
		t.Fatalf("pat=%v pmt=%v video=%v", sawPAT, sawPMT, sawVideo)
	}
	if len(sc.Events()) != 0 {
		t.Fatalf("clean stream must have no scanner events: %+v", sc.Events())
	}
}

func TestDeterministicOutput(t *testing.T) {
	a := BuildTier(Tier{Name: "1800k", BandwidthBPS: 1_800_000},
		6, 3*time.Second, Options{})
	b := BuildTier(Tier{Name: "1800k", BandwidthBPS: 1_800_000},
		6, 3*time.Second, Options{})
	if !bytes.Equal(a.Data, b.Data) {
		t.Fatal("generated streams are not deterministic")
	}
}

func TestBitrateWithinTolerance(t *testing.T) {
	stream := BuildTier(Tier{Name: "900k", BandwidthBPS: 900_000},
		12, 3*time.Second, Options{})
	bytesPerSeg := 0
	for _, r := range stream.Segments {
		bytesPerSeg += r.End - r.Start
	}
	bytesPerSeg /= 12
	got := float64(bytesPerSeg) * 8.0 / 3.0
	want := 900_000.0
	if diff := (got - want) / want; diff > 0.10 || diff < -0.10 {
		t.Fatalf("bitrate drift %.2f%% (got %.0f bps)", diff*100, got)
	}
}

func TestPTSBaseShiftsTimestamps(t *testing.T) {
	base := BuildTier(Tier{Name: "450k", BandwidthBPS: 450_000},
		2, 3*time.Second, Options{})
	shifted := BuildTier(Tier{Name: "450k", BandwidthBPS: 450_000},
		2, 3*time.Second, Options{PTSBase: 1_000_000})
	if bytes.Equal(base.Data, shifted.Data) {
		t.Fatal("PTS base shift must change the stream bytes")
	}
}
