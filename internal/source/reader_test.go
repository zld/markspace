package source

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/pion/rtp"

	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/frame"
)

func buildFrameStream(t *testing.T, n int) []byte {
	t.Helper()
	var out bytes.Buffer
	for i := 0; i < n; i++ {
		rec := &frame.Record{
			Version: frame.Version1, Track: "v", Seq: uint64(i),
			CaptureNS: int64(i) * 40_000_000, Width: 4, Height: 4,
			Color: frame.ColorMeta{ColorSpace: 2, Primaries: 1, Transfer: 1, FullRange: 1}, Payload: []byte{byte(i), 1, 2, 3},
		}
		raw, err := frame.MarshalRecord(rec)
		if err != nil {
			t.Fatal(err)
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		out.Write(lenBuf[:])
		out.Write(raw)
	}
	return out.Bytes()
}

func TestFrameReaderStream(t *testing.T) {
	data := buildFrameStream(t, 5)
	fr := NewFrameReader(bytes.NewReader(data), DefaultLimits())
	got := 0
	for {
		rec, err := fr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if rec.Seq != uint64(got) || rec.Offset <= 0 {
			t.Fatalf("bad record %+v", rec)
		}
		got++
	}
	if got != 5 || fr.Frames() != 5 || fr.Offset() != int64(len(data)) {
		t.Fatalf("got=%d frames=%d offset=%d total=%d", got, fr.Frames(), fr.Offset(), len(data))
	}
}

func TestFrameLimit(t *testing.T) {
	fr := NewFrameReader(bytes.NewReader(buildFrameStream(t, 8)), Limits{
		MaxFramePayload: 1 << 20, MaxFrames: 3,
	})
	for i := 0; i < 3; i++ {
		if _, err := fr.Next(); err != nil {
			t.Fatal(err)
		}
	}
	_, err := fr.Next()
	if !errors.Is(err, fault.ErrLimitExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestByteLimit(t *testing.T) {
	fr := NewFrameReader(bytes.NewReader(buildFrameStream(t, 10)), Limits{
		MaxFramePayload: 1 << 20, MaxTotalBytes: 10,
	})
	_, err := fr.Next()
	if !errors.Is(err, fault.ErrLimitExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestTruncatedStream(t *testing.T) {
	data := buildFrameStream(t, 3)
	fr := NewFrameReader(bytes.NewReader(data[:len(data)-7]), DefaultLimits())
	var sawFault bool
	for {
		_, err := fr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			sawFault = true
			if !errors.Is(err, fault.ErrTruncated) {
				t.Fatalf("got %v", err)
			}
			break
		}
	}
	if !sawFault {
		t.Fatal("truncation not surfaced")
	}
}

func buildRTPStream(t *testing.T, seqs []uint16) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, seq := range seqs {
		p := &rtp.Packet{
			Header:  rtp.Header{SequenceNumber: seq, SSRC: 42, PayloadType: 111, Timestamp: uint32(seq) * 480},
			Payload: []byte{0, 0, 0, 0},
		}
		raw, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		out.Write(lenBuf[:])
		out.Write(raw)
	}
	return out.Bytes()
}

func TestPacketReader(t *testing.T) {
	pr := NewPacketReader(bytes.NewReader(buildRTPStream(t, []uint16{5, 6, 4, 7})), 0)
	var seqs []uint16
	for {
		pkt, err := pr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if pkt.Offset < 0 {
			t.Fatal("missing offset")
		}
		seqs = append(seqs, pkt.SequenceNumber)
	}
	if len(seqs) != 4 || pr.Count() != 4 {
		t.Fatalf("seqs=%v count=%d", seqs, pr.Count())
	}
}
