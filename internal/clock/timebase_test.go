package clock

import "testing"

func TestToNanoseconds(t *testing.T) {
	got, err := RTP90K.ToNanoseconds(90_000)
	if err != nil {
		t.Fatal(err)
	}
	if got != NanosecondsPerSecond {
		t.Fatalf("90k tick second = %d ns, want %d", got, NanosecondsPerSecond)
	}

	got, err = PTS48K.ToNanoseconds(48)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(1_000_000); got != want {
		t.Fatalf("48 ticks at 48k = %d ns, want %d", got, want)
	}
}

func TestInvalidTimebase(t *testing.T) {
	if _, err := (Timebase{1, 0}).ToNanoseconds(1); err == nil {
		t.Fatal("zero denominator accepted")
	}
	if _, err := (Timebase{0, 1}).ToNanoseconds(1); err == nil {
		t.Fatal("zero numerator accepted")
	}
}
