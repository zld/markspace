package media

import (
	"errors"
	"testing"

	"github.com/pion/rtp"

	"example.com/assetpack/internal/fault"
)

func marshal(t *testing.T, seq uint16, ts uint32) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, SSRC: 0x11223344},
		Payload: []byte{0x01, 0x02, 0x03},
	}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRTPClockSequential(t *testing.T) {
	c, err := NewRTPClock(48000)
	if err != nil {
		t.Fatal(err)
	}
	// 20 ms cadence = 960 ticks.
	var last TimestampResult
	for i := 0; i < 5; i++ {
		res, err := c.Resolve(marshal(t, uint16(100+i), uint32(1000+960*i)), 48000)
		if err != nil {
			t.Fatal(err)
		}
		if res.Loss != 0 {
			t.Fatalf("unexpected loss at %d: %d", i, res.Loss)
		}
		last = res
	}
	if last.Nanos != 80*1e6 {
		t.Fatalf("4 packets at 20ms should be 80 ms, got %d ns", last.Nanos)
	}
	if last.Sample != 3840 {
		t.Fatalf("sample position %d, want 3840", last.Sample)
	}
	if c.Packets != 5 || c.LostPkts != 0 {
		t.Fatalf("counter mismatch: %+v", c)
	}
}

func TestRTPClockLossReported(t *testing.T) {
	c, _ := NewRTPClock(48000)
	if _, err := c.Resolve(marshal(t, 10, 0), 48000); err != nil {
		t.Fatal(err)
	}
	res, err := c.Resolve(marshal(t, 13, 3*960), 48000)
	if err != nil {
		t.Fatal(err)
	}
	if res.Loss != 2 {
		t.Fatalf("loss %d, want 2", res.Loss)
	}
	if c.LostPkts != 2 {
		t.Fatalf("total loss %d", c.LostPkts)
	}
}

func TestRTPClockTimestampWrap(t *testing.T) {
	c, _ := NewRTPClock(48000)
	nearMax := uint32(0xFFFFFF00)
	if _, err := c.Resolve(marshal(t, 1, nearMax), 48000); err != nil {
		t.Fatal(err)
	}
	res, err := c.Resolve(marshal(t, 2, 0x00000100), 48000)
	if err != nil {
		t.Fatalf("in-order wrap rejected: %v", err)
	}
	if res.Extended != (1<<32)+0x100 {
		t.Fatalf("extension %d", res.Extended)
	}
	if res.Nanos <= 0 {
		t.Fatalf("wrap produced non-positive time: %d", res.Nanos)
	}
}

func TestRTPClockRejectsDuplicateAndBackwards(t *testing.T) {
	c, _ := NewRTPClock(48000)
	if _, err := c.Resolve(marshal(t, 5, 960), 48000); err != nil {
		t.Fatal(err)
	}
	_, err := c.Resolve(marshal(t, 5, 960), 48000)
	var f *fault.Fault
	if !errors.As(err, &f) || f.Kind != fault.KindRTPInvalid {
		t.Fatalf("duplicate accepted: %v", err)
	}
	// Skipping ahead and then going back in time is not a wrap.
	if _, err := c.Resolve(marshal(t, 8, 4*960), 48000); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resolve(marshal(t, 9, 100), 48000); err == nil {
		t.Fatal("out-of-order backwards timestamp accepted")
	}
}

func TestRTPClockRejectsGarbage(t *testing.T) {
	c, _ := NewRTPClock(48000)
	if _, err := c.Resolve([]byte{0x00}, 48000); err == nil {
		t.Fatal("garbage RTP accepted")
	}
	if _, err := c.Accept(nil, 48000); err == nil {
		t.Fatal("nil packet accepted")
	}
	if _, err := NewRTPClock(0); err == nil {
		t.Fatal("zero clock rate accepted")
	}
}

func TestRTPMediaTime(t *testing.T) {
	c, _ := NewRTPClock(48000)
	if _, err := c.Resolve(marshal(t, 1, 48000), 48000); err != nil {
		t.Fatal(err)
	}
	pos := c.MediaTime(96000, 48000)
	if pos.Nanos != 1e9 || pos.Sample != 48000 {
		t.Fatalf("media time %+v", pos)
	}
}
