package media

import (
	"errors"
	"math/big"
	"testing"
)

func TestNewClockRejectsZero(t *testing.T) {
	if _, err := NewClock(0, 1); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("zero num: want ErrInvalidArgument, got %v", err)
	}
	if _, err := NewClock(1, 0); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("zero den: want ErrInvalidArgument, got %v", err)
	}
}

func TestClockToRational(t *testing.T) {
	// 90 kHz clock: 90000 ticks == 1 second.
	c, err := NewClock(90000, 1)
	if err != nil {
		t.Fatal(err)
	}
	one := c.ToRational(90000)
	if one.Cmp(big.NewRat(1, 1)) != 0 {
		t.Fatalf("90000 ticks = %v, want 1s", &one)
	}
	half := c.ToRational(45000)
	if half.Cmp(big.NewRat(1, 2)) != 0 {
		t.Fatalf("45000 ticks = %v, want 1/2s", &half)
	}
}

func TestClockCmpTick(t *testing.T) {
	c, _ := NewClock(30000, 1001) // NTSC-style rate
	cases := []struct {
		a, b int64
		want int
	}{
		{3000, 3000, 0},
		{3001, 3000, 1},
		{2999, 3000, -1},
		{-10, 10, -1},
	}
	for _, tc := range cases {
		if got := c.CmpTick(tc.a, tc.b); got != tc.want {
			t.Errorf("CmpTick(%d,%d)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestClockCmpRational(t *testing.T) {
	c, _ := NewClock(90000, 1)
	half := big.NewRat(1, 2)
	if c.CmpRational(45000, half) != 0 {
		t.Fatal("45000 ticks must equal 1/2 second")
	}
	if c.CmpRational(90001, half) <= 0 {
		t.Fatal("90001 ticks must exceed 1/2 second")
	}
	if c.CmpRational(1, half) >= 0 {
		t.Fatal("1 tick must be below 1/2 second")
	}
}

func TestByteRangeContains(t *testing.T) {
	r := ByteRange{Offset: 100, Length: 50}
	if !r.Contains(100) || !r.Contains(149) {
		t.Fatal("interior points must be contained")
	}
	if r.Contains(99) || r.Contains(150) {
		t.Fatal("boundary-adjacent points must be outside")
	}
}

func TestErrorClassAndExitCode(t *testing.T) {
	e := NewOffsetError(ClassRecoverable, "h264", "dangling NAL unit", 4096)
	if e.Error() == "" || e.Offset != 4096 {
		t.Fatalf("bad error rendering: %v", e)
	}
	if ClassOf(e) != ClassRecoverable || e.Class.ExitCode() != 3 {
		t.Fatalf("class/exit mapping wrong: %d %d", ClassOf(e), e.Class.ExitCode())
	}
	if ClassOf(errors.New("plain")) != ClassFatal {
		t.Fatal("plain errors must default to fatal")
	}
	wrapped := WrapError(ClassCheckpoint, "job", "digest mismatch", e)
	var target *Error
	if !errors.As(wrapped, &target) || target.Class != ClassCheckpoint {
		t.Fatal("error chain lost classification")
	}
}
