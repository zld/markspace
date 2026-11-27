package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// marshal builds a minimal RTP packet.
func marshal(seq uint16, ts uint32, payload []byte) []byte {
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0xCAFE},
		Payload: payload,
	}
	b, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

func pushSeqs(t *testing.T, r *Recoverer, seqs []uint16, size int) {
	t.Helper()
	for i, s := range seqs {
		payload := make([]byte, size)
		for j := range payload {
			payload[j] = byte(i + j)
		}
		if _, err := r.Push(marshal(s, uint32(i)*3000, payload), clock.Time(i)); err != nil {
			t.Fatalf("push %d: %v", s, err)
		}
	}
}

func TestBufferDeliversInOrder(t *testing.T) {
	r, err := NewRecoverer(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	pushSeqs(t, r, []uint16{1, 2, 3, 4}, 10)
	rep, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Packets != 4 || len(rep.Segments) != 1 {
		t.Fatalf("recovery = %+v", rep)
	}
	if rep.Segments[0].From != 1 || rep.Segments[0].To != 4 || len(rep.Bytes()) != 40 {
		t.Fatalf("segment = %+v bytes=%d", rep.Segments[0], len(rep.Bytes()))
	}
}

func TestBufferRecoversReorder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WaitWindow = 3
	r, _ := NewRecoverer(cfg)
	pushSeqs(t, r, []uint16{1, 2, 4, 3, 5}, 4)
	rep, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Recovered != 1 || rep.Missing != 0 || len(rep.Segments) != 1 {
		t.Fatalf("recovery = %+v", rep)
	}
}

func TestBufferGapAfterWindowContinues(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WaitWindow = 2
	r, _ := NewRecoverer(cfg)
	pushSeqs(t, r, []uint16{1, 2, 4, 5, 6, 7}, 4)
	rep, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Missing != 1 || rep.Packets != 6 {
		t.Fatalf("recovery = %+v", rep)
	}
	if len(rep.Faults) != 1 || rep.Faults[0].Code != fault.CodeRTPGapUnrecovered {
		t.Fatalf("faults = %+v", rep.Faults)
	}
	if len(rep.Segments) != 2 || rep.Segments[0].To != 2 || rep.Segments[1].From != 4 {
		t.Fatalf("segments = %+v", rep.Segments)
	}
}

func TestBufferDuplicateIdempotent(t *testing.T) {
	r, _ := NewRecoverer(DefaultConfig())
	pushSeqs(t, r, []uint16{1, 2, 2, 2, 3}, 8)
	rep, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Packets != 3 {
		t.Fatalf("packets = %d", rep.Packets)
	}
	if s := r.Stats(); s.Duplicates != 2 || s.Unique != 3 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestBufferSequenceWrap(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WaitWindow = 4
	r, _ := NewRecoverer(cfg)
	pushSeqs(t, r, []uint16{65534, 65535, 0, 1, 2, 3}, 2)
	rep, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Packets != 6 || rep.Missing != 0 || len(rep.Segments) != 1 {
		t.Fatalf("recovery = %+v", rep)
	}
	if rep.Segments[0].From != 65534 || rep.Segments[0].To != 0x10003 {
		t.Fatalf("range = %d..%d", rep.Segments[0].From, rep.Segments[0].To)
	}
}

func TestBufferWrapWithReorder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.WaitWindow = 4
	r, _ := NewRecoverer(cfg)
	pushSeqs(t, r, []uint16{65534, 0, 1, 65535, 2}, 2)
	rep, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Packets != 5 || rep.Missing != 0 || rep.Recovered != 1 {
		t.Fatalf("recovery = %+v", rep)
	}
}

func TestBufferLimitFails(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BufferLimit = 16
	r, _ := NewRecoverer(cfg)
	for _, seq := range []uint16{1, 3, 4, 5, 6} {
		if _, err := r.Push(marshal(seq, 0, make([]byte, 8)), 0); err != nil {
			f, ok := fault.As(err)
			if !ok || f.Code != fault.CodeRTPBufferLimit {
				t.Fatalf("err = %v", err)
			}
			return
		}
	}
	t.Fatal("expected buffer limit fault")
}

func TestBufferRejectsMalformed(t *testing.T) {
	r, _ := NewRecoverer(DefaultConfig())
	if _, err := r.Push([]byte{0x80}, 0); err == nil {
		t.Fatal("expected malformed fault")
	}
}

func TestBufferConfigValidation(t *testing.T) {
	if _, err := NewBuffer(Config{WaitWindow: -1, BufferLimit: 1, ClockRate: 90000}); err == nil {
		t.Fatal("negative window must fail")
	}
	if _, err := NewBuffer(Config{WaitWindow: 1, BufferLimit: 0, ClockRate: 1}); err == nil {
		t.Fatal("zero buffer must fail")
	}
}
