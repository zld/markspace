package clock

import "testing"

func TestMapperRemovesNonZeroOrigin(t *testing.T) {
	tb := TimeBase{TicksPerSecond: 90000}
	m := NewMapper(tb, 4294958295)
	if got := m.Relative(4294967295); got != 9000 {
		t.Fatalf("relative=%d want 9000", got)
	}
	if secs := m.Time(9000); !secs.Equal(NewRational(1, 10)) {
		t.Fatalf("9000 ticks=%s want 1/10s", secs)
	}
}

func TestConvertTicksBetweenTimeBases(t *testing.T) {
	movie := TimeBase{TicksPerSecond: 1000}
	media := TimeBase{TicksPerSecond: 48000}
	got, ok := ConvertTicks(100, movie, media)
	if !ok || got != 4800 {
		t.Fatalf("convert 100ms=%d,%v want 4800,true", got, ok)
	}
	if _, ok := ConvertTicks(1, movie, TimeBase{TicksPerSecond: 24}); ok {
		t.Fatal("1/1000s should not be integral at 24 ticks/s")
	}
}
