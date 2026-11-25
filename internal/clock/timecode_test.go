package clock

import (
	"strings"
	"testing"

	"archivecheck/internal/diagnostic"
)

func TestParseTimecodeIntegerRates(t *testing.T) {
	cases := []struct {
		token string
		rate  Rate
		want  Nanoseconds
	}{
		{"00:00:00:01", Rate24, 41_666_667},
		{"00:00:00:01", Rate25, 40_000_000},
		{"00:00:00:01", Rate50, 20_000_000},
		{"01:00:00:00", Rate25, Hour},
	}
	for _, tc := range cases {
		got, err := ParseTimecode(tc.token, tc.rate, 3, 100)
		if err != nil {
			t.Fatalf("ParseTimecode(%s/%s): %v", tc.token, tc.rate.Name, err)
		}
		if got != tc.want {
			t.Fatalf("%s %s = %d, want %d", tc.token, tc.rate.Name, got, tc.want)
		}
	}
}

func TestParseTimecode2997NDFAndDF(t *testing.T) {
	got, err := ParseTimecode("00:00:00:01", Rate2997N, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != 33_366_667 {
		t.Fatalf("NDF first frame = %d, want 33366667", got)
	}

	got, err = ParseTimecode("00:01:00;02", Rate2997D, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := Nanoseconds(60_060_000_000); got != want {
		t.Fatalf("DF 00:01:00;02 = %d, want %d", got, want)
	}

	if _, err := ParseTimecode("00:10:00;00", Rate2997D, 1, 0); err != nil {
		t.Fatalf("frame zero at ten minutes must be legal: %v", err)
	}
}

func TestParseTimecodeRejectsIllegalDFWithLocation(t *testing.T) {
	for _, token := range []string{"00:01:00;00", "00:01:00;01"} {
		_, err := ParseTimecode(token, Rate2997D, 9, 100)
		de, ok := diagnostic.From(err)
		if !ok || de.Code != diagnostic.CodeIllegalDropFrame {
			t.Fatalf("token %s error = %v, want ILLEGAL_DROP_FRAME", token, err)
		}
		if de.Sample != 9 || de.Offset != 109 {
			t.Fatalf("location = sample %d offset %d, want 9/109", de.Sample, de.Offset)
		}
	}
}

func TestParseTimecodeRejectsNegativeOverflowAndRange(t *testing.T) {
	_, err := ParseTimecode("-00:00:01:00", Rate25, 2, 4)
	if !diagnostic.HasCode(err, diagnostic.CodeNegativeTime) {
		t.Fatalf("negative error = %v", err)
	}
	if de, _ := diagnostic.From(err); de.Offset != 4 {
		t.Fatalf("negative offset = %d, want 4", de.Offset)
	}

	_, err = ParseTimecode("999999999999999999999:00:00:00", Rate25, 2, 8)
	if !diagnostic.HasCode(err, diagnostic.CodeOverflow) {
		t.Fatalf("overflow error = %v", err)
	}

	_, err = ParseTimecode("00:60:00:00", Rate25, 2, 8)
	if !diagnostic.HasCode(err, diagnostic.CodeInvalidTimecode) || !strings.Contains(err.Error(), "minute") {
		t.Fatalf("minute range error = %v", err)
	}

	_, err = ParseTimecode("00:00:00:25", Rate25, 2, 8)
	if !diagnostic.HasCode(err, diagnostic.CodeInvalidTimecode) {
		t.Fatalf("frame range error = %v", err)
	}

	_, err = ParseTimecode("00:01:00;02", Rate2997N, 2, 8)
	if !diagnostic.HasCode(err, diagnostic.CodeInvalidTimecode) {
		t.Fatalf("DF separator with NDF error = %v", err)
	}
}

func TestParseRate(t *testing.T) {
	for _, s := range []string{"24", "25", "29.97DF", "29.97NDF", "50"} {
		if _, err := ParseRate(s); err != nil {
			t.Fatalf("ParseRate(%q): %v", s, err)
		}
	}
	if _, err := ParseRate("23.976"); err == nil {
		t.Fatal("unsupported rate accepted")
	}
}
