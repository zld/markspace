package rtp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"testing"

	"github.com/pion/rtp"

	mpierr "mediapipeline/internal/errors"
)

// buildFrame constructs a valid checksummed media frame payload.
func buildFrame(t *testing.T, samples uint16, blockSize uint16, data []byte) []byte {
	t.Helper()
	if len(data)%int(blockSize) != 0 {
		t.Fatalf("data length %d not multiple of block size %d", len(data), blockSize)
	}
	blocks := len(data) / int(blockSize)
	out := make([]byte, 0, frameHeaderSize+len(data)+blocks*blockCheckSize)
	out = append(out, frameMagic0, frameMagic1, 1, 0)
	out = binary.BigEndian.AppendUint16(out, samples)
	out = binary.BigEndian.AppendUint16(out, blockSize)
	out = binary.BigEndian.AppendUint16(out, uint16(blocks))
	out = append(out, data...)
	for b := 0; b < blocks; b++ {
		lo := b * int(blockSize)
		out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(data[lo:lo+int(blockSize)]))
	}
	return out
}

func buildPacket(t *testing.T, seq uint16, ts uint32, frame []byte) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 1},
		Payload: frame,
	}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal rtp: %v", err)
	}
	return raw
}

func streamOf(packets ...[]byte) []byte {
	var buf bytes.Buffer
	for _, p := range packets {
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(p)))
		buf.Write(l[:])
		buf.Write(p)
	}
	return buf.Bytes()
}

func TestReadCleanStream(t *testing.T) {
	frame := buildFrame(t, 960, 1, []byte{10, 20, 30})
	raw := streamOf(
		buildPacket(t, 100, 0, frame),
		buildPacket(t, 101, 960, frame),
	)
	rd := NewReader(bytes.NewReader(raw), 48000)
	f0, err := rd.Read()
	if err != nil || f0.ExtSeq != 100 || f0.Samples != 960 || f0.StreamOffset != 0 {
		t.Fatalf("first frame: %+v err=%v", f0, err)
	}
	f1, err := rd.Read()
	if err != nil || f1.ExtSeq != 101 || f1.StreamOffset == 0 {
		t.Fatalf("second frame: %+v err=%v", f1, err)
	}
	if _, err := rd.Read(); !errors.Is(err, io.EOF) {
		t.Fatalf("end of stream err = %v, want io.EOF", err)
	}
}

func TestCorruptionLocalizedToOneByte(t *testing.T) {
	data := []byte{7, 8, 9, 10}
	frame := buildFrame(t, 480, 1, data)
	pkt := buildPacket(t, 5, 0, frame)
	raw := streamOf(pkt)

	// Corrupt the third data byte. Its stream position includes the 2-byte
	// length prefix; bad[] holds the whole stream, so add 2 to the index.
	bad := bytes.Clone(raw)
	hdrLen := len(pkt) - len(frame)
	corruptAt := hdrLen + frameHeaderSize + 2
	bad[corruptAt+2] ^= 0xFF

	rd := NewReader(bytes.NewReader(bad), 48000)
	_, err := rd.Read()
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatCorrupt {
		t.Fatalf("err = %v, want CatCorrupt", err)
	}
	// Stream offset = 2-byte length prefix + RTP header (measured) +
	// frame header + index of the corrupted data byte.
	want := int64(2) + int64(hdrLen) + int64(frameHeaderSize) + 2
	if me.ByteOffset != want {
		t.Fatalf("corrupt offset = %d, want %d (localization error must be <=1 byte)", me.ByteOffset, want)
	}
}

func TestTruncatedPacket(t *testing.T) {
	frame := buildFrame(t, 1, 1, []byte{1})
	pkt := buildPacket(t, 1, 0, frame)
	// Declare a longer length than the bytes actually present.
	var raw bytes.Buffer
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(pkt)+4))
	raw.Write(l[:])
	raw.Write(pkt)
	rd := NewReader(&raw, 48000)
	_, err := rd.Read()
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatTruncated {
		t.Fatalf("err = %v, want CatTruncated", err)
	}
}

func TestTrackerRolloverReorderDuplicate(t *testing.T) {
	tr := NewTracker()
	r := func(got TrackResult, seq uint64) {
		t.Helper()
		if got.Seq != seq {
			t.Fatalf("ext seq = %d, want %d", got.Seq, seq)
		}
	}
	r(tr.Push(65534), 65534)
	r(tr.Push(65535), 65535)
	r(tr.Push(0), 65536) // rollover
	r(tr.Push(1), 65537)
	// Late packet from the previous cycle (seq 65535, ext 65535).
	got := tr.Push(65535)
	if !got.Duplicate {
		t.Fatal("repeated 65535 must be flagged duplicate")
	}
	// Reordered arrival: 3 then 2.
	g3 := tr.Push(3)
	r(g3, 65539)
	g2 := tr.Push(2)
	r(g2, 65538)
	if g2.Duplicate {
		t.Fatal("seq 2 arrives reordered but must not be a duplicate")
	}
	if tr.LastExt() != 65539 || tr.BaseExt() != 65534 {
		t.Fatalf("state = last %d base %d", tr.LastExt(), tr.BaseExt())
	}
}

func TestOversizedPacketRejected(t *testing.T) {
	var raw bytes.Buffer
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], 0) // zero length violates [1, MaxPacketBytes]
	raw.Write(l[:])
	rd := NewReader(&raw, 48000)
	_, err := rd.Read()
	var me *mpierr.MediaError
	if !errors.As(err, &me) || me.Category != mpierr.CatLimit {
		t.Fatalf("err = %v, want CatLimit", err)
	}
}
