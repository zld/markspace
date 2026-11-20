package ts

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"hlscheck/internal/model"
)

func TestPTSUnwrapper(t *testing.T) {
	u := &PTSUnwrapper{}
	cases := []struct {
		name string
		raw  int64
		want int64
	}{
		{"origin", 1000, 1000},
		{"steady", 4600, 4600},
		{"steady2", 8200, 8200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := u.Push(tc.raw); got != tc.want {
				t.Fatalf("Push(%d) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// TestPTSUnwrapperWraparound walks adjacent frame-sized steps across the
// 2^33 modulus boundary. Each consecutive raw value differs by one frame
// (3600 ticks), which is the premise the shortest-delta join relies on.
func TestPTSUnwrapperWraparound(t *testing.T) {
	u := &PTSUnwrapper{}
	steps := []struct {
		raw  int64
		want int64
	}{
		{PTSModulus - 7200, PTSModulus - 7200},
		{PTSModulus - 3600, PTSModulus - 3600},
		{0, PTSModulus}, // forward wrap: raw reset to zero
		{3600, PTSModulus + 3600},
		{7200, PTSModulus + 7200},
	}
	for _, tc := range steps {
		if got := u.Push(tc.raw); got != tc.want {
			t.Fatalf("Push(%d) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

// TestPTSUnwrapperBackwardStep verifies a small backward correction is
// preserved as a negative delta rather than expanded into a forward wrap.
func TestPTSUnwrapperBackwardStep(t *testing.T) {
	u := &PTSUnwrapper{}
	u.Push(7200)
	if got := u.Push(3600); got != 3600 {
		t.Fatalf("backward step = %d, want 3600", got)
	}
}

func TestContinuityTracker(t *testing.T) {
	tracker := NewContinuityTracker()
	pid := 0x0100
	mk := func(cc int, adaptation int, discont bool) *Packet {
		p := &Packet{PID: pid, AdaptationControl: adaptation, ContinuityCounter: cc}
		if discont {
			p.Discontinuity = true
		}
		return p
	}
	if ev, _ := tracker.Observe(mk(0, 1, false)); ev != CCOk {
		t.Fatalf("first packet: %s", ev)
	}
	if ev, _ := tracker.Observe(mk(1, 1, false)); ev != CCOk {
		t.Fatalf("increment: %s", ev)
	}
	if ev, _ := tracker.Observe(mk(1, 1, false)); ev != CCDuplicate {
		t.Fatalf("repeat with payload: %s, want duplicate", ev)
	}
	// Adaptation-only packets legally reuse the counter.
	if ev, _ := tracker.Observe(mk(2, 2, false)); ev != CCOk {
		t.Fatalf("adaptation only: %s", ev)
	}
	// Declared discontinuity resets expectations instead of flagging a jump.
	if ev, _ := tracker.Observe(mk(9, 1, true)); ev != CCDeclared {
		t.Fatalf("declared: %s", ev)
	}
	if ev, want := tracker.Observe(mk(12, 1, false)); ev != CCJump || want != 10 {
		t.Fatalf("undeclared jump: ev=%s wantCC=%d", ev, want)
	}
	// Null PID never participates.
	null := &Packet{PID: PIDNull, AdaptationControl: 1, ContinuityCounter: 7}
	if ev, _ := tracker.Observe(null); ev != CCOk {
		t.Fatalf("null pid: %s", ev)
	}
}

func TestPacketReaderSyncAndOffset(t *testing.T) {
	good := make([]byte, model.PacketSize*2)
	for i := range good {
		if i%model.PacketSize == 0 {
			good[i] = model.SyncByte
		}
	}
	pr := NewPacketReader(bytes.NewReader(good))
	p0, err := pr.ReadPacket()
	if err != nil {
		t.Fatalf("first packet: %v", err)
	}
	if p0.Offset != 0 {
		t.Fatalf("offset = %d, want 0", p0.Offset)
	}
	p1, err := pr.ReadPacket()
	if err != nil || p1.Offset != model.PacketSize {
		t.Fatalf("second packet offset=%d err=%v", p1.Offset, err)
	}
	if _, err := pr.ReadPacket(); !errors.Is(err, io.EOF) {
		t.Fatalf("end = %v, want EOF", err)
	}
}

func TestPacketReaderBadSyncReportsOffset(t *testing.T) {
	// First packet valid, second has a corrupt sync byte at offset 188.
	raw := make([]byte, model.PacketSize*2)
	raw[0] = model.SyncByte
	raw[model.PacketSize] = 0x00
	pr := NewPacketReader(bytes.NewReader(raw))
	if _, err := pr.ReadPacket(); err != nil {
		t.Fatalf("first packet: %v", err)
	}
	_, err := pr.ReadPacket()
	if !errors.Is(err, ErrSyncByte) {
		t.Fatalf("err = %v, want ErrSyncByte", err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte("offset 188")) {
		t.Fatalf("error %q does not locate offset 188", err.Error())
	}
}

func TestPacketReaderStitchesAcrossChunks(t *testing.T) {
	// A reader that hands back one byte at a time must still assemble a
	// full 188-byte packet via the residual buffer.
	raw := make([]byte, model.PacketSize)
	raw[0] = model.SyncByte
	pr := NewPacketReader(&byteReader{data: raw})
	p, err := pr.ReadPacket()
	if err != nil {
		t.Fatalf("stitched read: %v", err)
	}
	if p.Offset != 0 || len(p.Raw()) != model.PacketSize {
		t.Fatalf("packet offset=%d len=%d", p.Offset, len(p.Raw()))
	}
}

func TestPacketReaderTruncatedTail(t *testing.T) {
	raw := make([]byte, model.PacketSize+7)
	raw[0] = model.SyncByte
	pr := NewPacketReader(bytes.NewReader(raw))
	if _, err := pr.ReadPacket(); err != nil {
		t.Fatalf("full packet: %v", err)
	}
	if _, err := pr.ReadPacket(); !errors.Is(err, ErrShortPacket) {
		t.Fatalf("tail = %v, want ErrShortPacket", err)
	}
}

// byteReader yields a single byte per Read to force residual handling.
type byteReader struct{ data []byte }

func (b *byteReader) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	p[0] = b.data[0]
	b.data = b.data[1:]
	return 1, nil
}
