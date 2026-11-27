package clock

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func TestRTPOutOfOrderStableOrigin(t *testing.T) {
	tl := NewTimeline(0)
	const ssrc uint32 = 7
	if err := tl.RegisterStream(ssrc, 48000); err != nil {
		t.Fatal(err)
	}
	n0, err := tl.ObserveRTP(ssrc, 1000, 0)
	if err != nil || n0 != 0 {
		t.Fatalf("origin = %d, err=%v", n0, err)
	}
	if _, err := tl.ObserveRTP(ssrc, 2000, 1); err != nil {
		t.Fatal(err)
	}
	// Reordered older packet must resolve against the fixed origin.
	n2, err := tl.ObserveRTP(ssrc, 1500, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := int64(500) * NanosPerSecond / 48000
	if n2 != want {
		t.Fatalf("reordered mapping = %d, want %d", n2, want)
	}
	// Origin packet arriving again stays at zero.
	n3, err := tl.ObserveRTP(ssrc, 1000, 3)
	if err != nil || n3 != 0 {
		t.Fatalf("duplicate origin = %d, err=%v", n3, err)
	}
}

func TestRTPTimestampWrap(t *testing.T) {
	tl := NewTimeline(0)
	const ssrc uint32 = 1
	if err := tl.RegisterStream(ssrc, 48000); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.ObserveRTP(ssrc, 0xFFFFFF00, 0); err != nil {
		t.Fatal(err)
	}
	n, err := tl.ObserveRTP(ssrc, 0x0010, 1)
	if err != nil {
		t.Fatalf("wrap packet rejected: %v", err)
	}
	want := int64(272) * NanosPerSecond / 48000
	if n != want {
		t.Fatalf("wrap mapping = %d, want %d", n, want)
	}
}

func TestZeroClockRateAndUnknownSSRC(t *testing.T) {
	tl := NewTimeline(0)
	if err := tl.RegisterStream(9, 0); err == nil {
		t.Fatal("zero clock rate must fail")
	} else if ce, ok := AsError(err); !ok || ce.Category != CatInvalidParam || ce.Message == "" {
		t.Fatalf("bad structured error: %v", err)
	}
	if _, err := tl.ObserveRTP(42, 10, 5); err == nil {
		t.Fatal("unknown ssrc must fail")
	} else if ce, ok := AsError(err); !ok || !ce.HasSeq || ce.SeqNum != 5 ||
		ce.Category != CatUnmappableTime {
		t.Fatalf("error must carry seq=5: %v", err)
	}
}

func TestOverflowDetected(t *testing.T) {
	tl := NewTimeline(8000)
	if _, err := tl.SampleToNanos(1 << 62); err == nil {
		t.Fatal("huge sample index must overflow")
	} else if ce, ok := AsError(err); !ok || ce.Category != CatOverflow {
		t.Fatalf("want overflow category, got %v", err)
	}
	if _, err := ticksToNanos(math.MaxInt64, 1); err == nil {
		t.Fatal("max ticks must overflow")
	}
}

func TestSampleNanosRoundTrip(t *testing.T) {
	tl := NewTimeline(48000)
	// Whole-second boundaries convert exactly in both directions.
	for _, idx := range []int64{0, 48000, 96000} {
		n, err := tl.SampleToNanos(idx)
		if err != nil {
			t.Fatal(err)
		}
		back, err := tl.NanosToSample(n)
		if err != nil || back != idx {
			t.Fatalf("round trip idx=%d ns=%d back=%d err=%v", idx, n, back, err)
		}
	}
	// Sub-sample nanoseconds floor to a complete sample.
	if back, err := tl.NanosToSample(20833); err != nil || back != 0 {
		t.Fatalf("floor semantics = %d, err=%v", back, err)
	}
	if back, err := tl.NanosToSample(NanosPerSecond + 20833); err != nil || back != 48000 {
		t.Fatalf("floor at one second = %d, err=%v", back, err)
	}
}

// buildMinimalMP4 synthesizes ftyp + moov/mvhd(v0) with the given timescale.
func buildMinimalMP4(t *testing.T, timescale uint32) *bytes.Reader {
	t.Helper()
	mvhd := make([]byte, 100) // fixed-size v0 mvhd payload
	be := binary.BigEndian
	be.PutUint32(mvhd[0:], 0) // version 0 + flags
	be.PutUint32(mvhd[4:], 0) // creation
	be.PutUint32(mvhd[8:], 0) // modification
	be.PutUint32(mvhd[12:], timescale)
	be.PutUint32(mvhd[16:], 1000) // duration
	be.PutUint32(mvhd[20:], 0x00010000)
	be.PutUint16(mvhd[24:], 0x0100)
	// 26:28 reserved, 28:36 reserved2, 36:72 matrix, 72:96 predefined
	be.PutUint32(mvhd[96:], 2) // next track ID
	moov := box("moov", box("mvhd", mvhd))
	ftypPayload := make([]byte, 16)
	copy(ftypPayload, "isom")
	ftyp := box("ftyp", ftypPayload)
	return bytes.NewReader(append(ftyp, moov...))
}

func box(typ string, payload []byte) []byte {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, uint32(8+len(payload)))
	copy(hdr[4:], typ)
	return append(hdr, payload...)
}

func TestReadMP4Timescale(t *testing.T) {
	r := buildMinimalMP4(t, 90000)
	ts, err := ReadMP4Timescale(r)
	if err != nil {
		t.Fatalf("mp4 parse: %v", err)
	}
	if ts != 90000 {
		t.Fatalf("timescale = %d", ts)
	}
	n, err := MP4TickToNanos(90000, ts)
	if err != nil || n != NanosPerSecond {
		t.Fatalf("tick conv = %d, err=%v", n, err)
	}
	if _, err := MP4TickToNanos(1, 0); err == nil {
		t.Fatal("zero timescale must fail")
	}
	// Truncated garbage yields a structured not-found/truncated error.
	if _, err := ReadMP4Timescale(bytes.NewReader([]byte("garbage"))); err == nil {
		t.Fatal("garbage input must fail")
	}
}
