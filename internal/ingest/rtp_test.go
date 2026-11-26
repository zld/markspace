package ingest

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"pwf/internal/fixture"
	"pwf/internal/media"
)

// frameWriter collects length-prefixed frames exactly like StreamRTP expects.
func frameWriter(t *testing.T, packets [][]byte) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	var l [2]byte
	for _, p := range packets {
		binary.BigEndian.PutUint16(l[:], uint16(len(p)))
		buf.Write(l[:])
		buf.Write(p)
	}
	return &buf
}

func TestStreamRTPBasicAndGaps(t *testing.T) {
	var packets [][]byte
	for seq := uint16(100); seq < 110; seq++ {
		if seq == 104 || seq == 105 { // two-packet gap
			continue
		}
		packets = append(packets, fixture.RTPPacket(1, seq, uint32(seq)*960, 96,
			fixture.OpusPayload(12, 0, 40)))
	}
	res, err := StreamRTP(frameWriter(t, packets), "t1", CodecOpus, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 8 {
		t.Fatalf("expected 8 accepted records, got %d", len(res.Records))
	}
	if len(res.Gaps) != 1 || res.Gaps[0].StartSeq != 104 || res.Gaps[0].Count != 2 {
		t.Fatalf("gap detection wrong: %+v", res.Gaps)
	}
	// Every packet is 20ms Opus; total must be 8 * 960 ticks exactly.
	var total int64
	for _, r := range res.Records {
		total += r.Duration.Ticks
	}
	if total != 8*960 {
		t.Fatalf("integral duration total wrong: %d", total)
	}
}

func TestStreamRTPDuplicatesNotDoubleTimed(t *testing.T) {
	var packets [][]byte
	for seq := uint16(0); seq < 5; seq++ {
		pkt := fixture.RTPPacket(1, seq, uint32(seq)*960, 96, fixture.OpusPayload(12, 0, 40))
		packets = append(packets, pkt)
		if seq == 2 {
			packets = append(packets, pkt) // exact duplicate
		}
	}
	res, err := StreamRTP(frameWriter(t, packets), "t1", CodecOpus, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if res.DupCount != 1 {
		t.Fatalf("expected 1 duplicate, got %d", res.DupCount)
	}
	if len(res.Records) != 5 {
		t.Fatalf("duplicate must not add a record, got %d", len(res.Records))
	}
}

func TestStreamRTPWrapAndOutOfOrder(t *testing.T) {
	// Sequences 65534, 65535 then 0, 1 -> extended 65534..65537.
	seqs := []uint16{65534, 65535, 0, 1}
	var packets [][]byte
	for _, seq := range seqs {
		packets = append(packets, fixture.RTPPacket(1, seq, uint32(seq), 96,
			fixture.OpusPayload(12, 0, 40)))
	}
	// Reorder: deliver 0 before 65535 to exercise out-of-order handling.
	packets[1], packets[2] = packets[2], packets[1]
	res, err := StreamRTP(frameWriter(t, packets), "wrap", CodecOpus, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 4 {
		t.Fatalf("expected 4 records across wrap, got %d", len(res.Records))
	}
	want := []uint64{65534, 65536, 65535, 65537}
	for i, r := range res.Records {
		if r.ExtSequence != want[i] {
			t.Fatalf("record %d ext seq got %d want %d", i, r.ExtSequence, want[i])
		}
	}
	if res.OutOfOrder < 1 {
		t.Fatalf("expected out-of-order count >=1, got %d", res.OutOfOrder)
	}
}

func TestStreamRTPOffsetPrecision(t *testing.T) {
	var packets [][]byte
	for seq := uint16(0); seq < 4; seq++ {
		packets = append(packets, fixture.RTPPacket(1, seq, 0, 96, fixture.OpusPayload(12, 0, 40)))
	}
	buf := frameWriter(t, packets)
	res, err := StreamRTP(bytes.NewReader(buf.Bytes()), "off", CodecOpus, 48000)
	if err != nil {
		t.Fatal(err)
	}
	// Each frame: 2-byte prefix + (12-byte header + 40-byte payload).
	frameLen := 2 + 12 + 40
	for i, r := range res.Records {
		if r.Offset != int64(i*frameLen) {
			t.Fatalf("record %d offset got %d want %d", i, r.Offset, i*frameLen)
		}
	}
}

func TestStreamRTPTruncatedHeaderReportsOffset(t *testing.T) {
	pkt := fixture.RTPPacket(1, 0, 0, 96, fixture.OpusPayload(12, 0, 40))
	var buf bytes.Buffer
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(pkt)))
	buf.Write(l[:])
	buf.Write(pkt[:6]) // declared full length but only 6 header bytes present
	_, err := StreamRTP(bytes.NewReader(buf.Bytes()), "bad", CodecOpus, 48000)
	me, ok := media.AsError(err)
	if !ok {
		t.Fatalf("expected media.Error, got %v", err)
	}
	// Truncated packet data is reported at the data start, past the prefix.
	if me.Offset != 2 || me.Stage != media.StageParseRTP {
		t.Fatalf("offset/stage wrong: %+v", me)
	}
	if !strings.Contains(me.Error(), "offset=2") {
		t.Fatalf("error must render offset: %s", me.Error())
	}
}

func TestStreamRTPRejectsOversizedLength(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0xff, 0xff}) // 65535 bytes, under MaxFrameBytes but absent
	_, err := StreamRTP(bytes.NewReader(buf.Bytes()), "big", CodecOpus, 48000)
	if err == nil {
		t.Fatal("truncated oversized frame must error")
	}
}

func TestStreamRTPEmptyStream(t *testing.T) {
	_, err := StreamRTP(bytes.NewReader(nil), "empty", CodecOpus, 48000)
	if _, ok := media.AsError(err); !ok {
		t.Fatalf("empty stream must be media.Error, got %v", err)
	}
}
