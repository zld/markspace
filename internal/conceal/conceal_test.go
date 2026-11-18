package conceal

import (
	"testing"

	"jitterlab/internal/packet"
)

func realFrame(sample int8) packet.Frame {
	f := packet.Frame{Version: packet.Version}
	for i := range f.Payload {
		f.Payload[i] = byte(sample)
	}
	return f
}

func TestRejectsBadConfig(t *testing.T) {
	if _, err := New(0, 0.9); err == nil {
		t.Fatal("accepted maxRun=0")
	}
	for _, d := range []float64{0, -0.5, 1.5} {
		if _, err := New(3, d); err == nil {
			t.Fatalf("accepted decay=%g", d)
		}
	}
}

func TestConcealExtendsThenDecaysThenMutes(t *testing.T) {
	c, err := New(4, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, recovered := c.Present(realFrame(100)); recovered {
		t.Fatal("first real frame reported recovery")
	}

	first := c.Conceal()
	if first.Kind != KindExtend || first.Run != 1 || first.Attenuation != 1 {
		t.Fatalf("first conceal = %+v", first)
	}
	if first.Payload[0] != 100 {
		t.Fatalf("extension payload = %d, want 100", first.Payload[0])
	}

	second := c.Conceal()
	if second.Kind != KindDecay || second.Run != 2 {
		t.Fatalf("second conceal = %+v", second)
	}
	if second.Payload[0] != 50 {
		t.Fatalf("decayed payload = %d, want 50", second.Payload[0])
	}

	wantPayload := []byte{25, 13}      // 0.25x and 0.125x of 100, rounded
	for i, want := range wantPayload { // runs 3 and 4 stay within the limit
		out := c.Conceal()
		if out.Kind != KindDecay || out.Run != 3+i {
			t.Fatalf("conceal %d = %+v", 3+i, out)
		}
		if out.Payload[0] != want {
			t.Fatalf("run %d payload = %d, want %d", 3+i, out.Payload[0], want)
		}
	}

	mute := c.Conceal()
	if mute.Kind != KindSilence || mute.Run != 5 {
		t.Fatalf("past limit = %+v, want silence run 5", mute)
	}
	if mute.Payload != [packet.PayloadSize]byte{} {
		t.Fatal("silence payload is not zero")
	}
}

func TestConcealWithoutRealFrameIsSilent(t *testing.T) {
	c, _ := New(4, 0.8)
	out := c.Conceal()
	if out.Kind != KindSilence || out.Run != 1 {
		t.Fatalf("conceal before audio = %+v", out)
	}
	if c.HasFrame() {
		t.Fatal("HasFrame true without a real frame")
	}
}

func TestRecoveryDeltaAndReset(t *testing.T) {
	c, _ := New(10, 0.5)
	c.Present(realFrame(10))
	c.Conceal() // extension of 10
	c.Conceal() // decay to 5

	out, delta, recovered := c.Present(realFrame(25))
	if !recovered || out.Kind != KindReal {
		t.Fatalf("recovery = %+v recovered=%v", out, recovered)
	}
	// Last concealed payload is 5, real frame is 25 => |25-5| = 20.
	if delta != 20 {
		t.Fatalf("delta = %v, want 20", delta)
	}
	if c.Run() != 0 {
		t.Fatalf("run after recovery = %d, want 0", c.Run())
	}

	next := c.Conceal()
	if next.Kind != KindExtend || next.Payload[0] != 25 {
		t.Fatalf("re-extend = %+v, want fresh extension of 25", next)
	}
}

func TestNegativeSamplesDoNotWrap(t *testing.T) {
	c, _ := New(4, 0.5)
	f := realFrame(-100)
	c.Present(f)
	out := c.Conceal() // run 1 full gain
	if int8(out.Payload[0]) != -100 {
		t.Fatalf("clipped positive: %d", int8(out.Payload[0]))
	}
	out = c.Conceal() // run 2 half gain
	if int8(out.Payload[0]) != -50 {
		t.Fatalf("decayed negative = %d, want -50", int8(out.Payload[0]))
	}
}

func TestAttenuationIsMonotonic(t *testing.T) {
	c, _ := New(6, 0.7)
	c.Present(realFrame(120))
	prev := 121.0
	for i := 0; i < 6; i++ {
		out := c.Conceal()
		if out.Kind == KindSilence {
			break
		}
		v := float64(out.Payload[0])
		if v > prev {
			t.Fatalf("conceal %d payload %v grew from %v", i, v, prev)
		}
		prev = v
	}
}
