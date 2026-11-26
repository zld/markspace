package repair_test

import (
	"testing"

	"github.com/pion/rtp"

	"mediarepair/internal/media"
	"mediarepair/internal/repair"
	"mediarepair/internal/testfixture"
)

func TestRepairStandardManifestFindsTwoDefects(t *testing.T) {
	root := t.TempDir()
	fx, err := testfixture.CreateStandard(root)
	if err != nil {
		t.Fatal(err)
	}
	rep := repair.RepairManifest(fx.Manifest, 1000)
	if rep.RepairedCount != 2 {
		t.Fatalf("repaired=%d want 2; report=%+v", rep.RepairedCount, rep)
	}
	if len(rep.Frames) != 12 {
		t.Fatalf("frames=%d want 12", len(rep.Frames))
	}
	for i := 1; i < len(rep.Frames); i++ {
		if rep.Frames[i].RepairedTimeUS <= rep.Frames[i-1].RepairedTimeUS {
			t.Fatalf("repaired times not monotonic at %d", i)
		}
	}
	if rep.MaxDeviationUS < 20000 {
		t.Fatalf("max deviation=%d expected around 20833us", rep.MaxDeviationUS)
	}
}

func TestRTPWraparoundUniqueAndHalfPeriodAmbiguous(t *testing.T) {
	got, err := repair.UnwrapRTPTimestamps([]uint32{0xfffffff0, 5, 10})
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{0xfffffff0, 1<<32 + 5, 1<<32 + 10}
	if len(got) != len(want) {
		t.Fatalf("expanded=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expanded[%d]=%d want %d all=%v", i, got[i], want[i], got)
		}
	}

	_, err = repair.UnwrapRTPTimestamps([]uint32{100, 100 + 1<<31})
	if !media.HasCode(err, media.CodeRTPAmbiguous) {
		t.Fatalf("half-period err=%v", err)
	}
}

func TestPionRTPPacketParseAndUnwrap(t *testing.T) {
	values := []uint32{0xfffffffe, 2}
	var packets [][]byte
	for i, ts := range values {
		p := &rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: uint16(i + 1), Timestamp: ts, Marker: i == 1},
			Payload: []byte{0x80, 0x01},
		}
		b, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, b)
	}
	expanded, markers, err := repair.ParseAndUnwrapRTPPackets(packets)
	if err != nil {
		t.Fatal(err)
	}
	if expanded[1] != 1<<32+2 || !markers[1] || markers[0] {
		t.Fatalf("expanded=%v markers=%v", expanded, markers)
	}
}
