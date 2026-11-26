package ingest

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/pion/rtp"

	"mediasync/internal/media"
)

func makeRTP(t *testing.T, seq uint16, ts uint32) []byte {
	t.Helper()
	p := rtp.Packet{Header: rtp.Header{
		Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 7,
	}, Payload: []byte{byte(seq), 0x01}}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func container(records ...[]byte) []byte {
	out := make([]byte, 0)
	for _, rec := range records {
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(rec)))
		out = append(out, l[:]...)
		out = append(out, rec...)
	}
	return out
}

func TestExtendSequenceWrap(t *testing.T) {
	prev := int64(65534)
	for _, want := range []int64{65535, 65536, 65537, 65538} {
		var seq uint16
		switch want {
		case 65535:
			seq = 65535
		case 65536:
			seq = 0
		case 65537:
			seq = 1
		default:
			seq = 2
		}
		got := ExtendSequence(prev, true, seq)
		if got != want {
			t.Fatalf("extend(%d)=%d want %d", seq, got, want)
		}
		prev = got
	}
}

func TestRTPReorderDuplicateAndGapWindow(t *testing.T) {
	// 10 datagrams: nine unique sequences in 1000..1009, missing 1005,
	// duplicate 1002, and 1004 arrives before 1003.
	order := []uint16{1000, 1001, 1002, 1002, 1004, 1003, 1006, 1007, 1008, 1009}
	recs := make([][]byte, 0, len(order))
	for i, seq := range order {
		recs = append(recs, makeRTP(t, seq, uint32(i*960)))
	}
	packets, stats, err := ParseRTPContainer(container(recs...), "opus", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 9 || stats.Duplicates != 1 || stats.Gaps != 1 {
		t.Fatalf("unique=%d dup=%d gaps=%d", len(packets), stats.Duplicates, stats.Gaps)
	}
	if len(stats.GapSequences) != 1 || stats.GapSequences[0] != 1005 {
		t.Fatalf("gaps=%v want [1005]", stats.GapSequences)
	}
	if packets[0].ExtSequence != 1000 || packets[len(packets)-1].ExtSequence != 1009 {
		t.Fatalf("packets not sorted: first=%d last=%d", packets[0].ExtSequence, packets[len(packets)-1].ExtSequence)
	}
	for i, p := range packets {
		if p.ExtSequence == 1005 {
			t.Fatal("missing packet must not be synthesized")
		}
		if i > 0 && p.ExtSequence < packets[i-1].ExtSequence {
			t.Fatal("packets not deterministic sorted")
		}
	}
}

func TestRTPWrapHasNoFalseGap(t *testing.T) {
	order := []uint16{65534, 65535, 0, 1}
	recs := make([][]byte, 0, len(order))
	for _, seq := range order {
		recs = append(recs, makeRTP(t, seq, uint32(seq)*960))
	}
	packets, stats, err := ParseRTPContainer(container(recs...), "opus", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) != 4 || stats.Gaps != 0 {
		t.Fatalf("packets=%d gaps=%d stats=%+v", len(packets), stats.Gaps, stats)
	}
	if packets[2].ExtSequence != 65536 {
		t.Fatalf("wrapped ext=%d", packets[2].ExtSequence)
	}
}

func TestRTPIllegalVersionReportsCoordinates(t *testing.T) {
	raw := makeRTP(t, 7, 0)
	raw[0] &= 0x3f // clear version bits -> version 0
	_, _, err := ParseRTPContainer(container(raw), "opus-bad", 0, 4)
	var fe *media.Error
	if !errors.As(err, &fe) || fe.Kind != media.KindInput {
		t.Fatalf("expected structured input error, got %#v", err)
	}
	if fe.Phase != "ingest.rtp" || fe.PacketIndex == nil || *fe.PacketIndex != 0 || fe.ByteOffset == nil || *fe.ByteOffset != 2 {
		t.Fatalf("bad coordinates: %+v", fe)
	}
	if media.ExitCode(err) != 2 {
		t.Fatalf("exit=%d", media.ExitCode(err))
	}
}

func TestRTPTruncatedRecordReportsOffset(t *testing.T) {
	raw := makeRTP(t, 7, 0)
	buf := container(raw)
	_, _, err := ParseRTPContainer(buf[:len(buf)-3], "opus-trunc", 0, 4)
	var fe *media.Error
	if !errors.As(err, &fe) || fe.ByteOffset == nil {
		t.Fatalf("expected byte offset fault, got %v", err)
	}
	if fe.PacketIndex == nil || *fe.PacketIndex != 0 {
		t.Fatalf("packet index=%v", fe.PacketIndex)
	}
}
