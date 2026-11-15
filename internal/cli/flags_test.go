package cli

import "testing"

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"12s", 12000},
		{"2s", 2000},
		{"500ms", 500},
		{"0.5s", 500},
		{"12.025s", 12025},
	}
	for _, c := range cases {
		got, err := ParseDuration(c.in)
		if err != nil {
			t.Fatalf("ParseDuration(%q) error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseDuration(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "12", "1.2.3s", "0.1234s", "-2s", "abc"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) expected error", bad)
		}
	}
}

func TestParseBitratesAndFaults(t *testing.T) {
	br, err := ParseBitrates("800000, 1600000")
	if err != nil || len(br) != 2 || br[0] != 800000 || br[1] != 1600000 {
		t.Fatalf("ParseBitrates unexpected: %v %v", br, err)
	}
	if _, err := ParseBitrates("0,100"); err == nil {
		t.Error("zero bitrate must be rejected")
	}
	faults, err := ParseFaults("missing, order,drift")
	if err != nil || len(faults) != 3 {
		t.Fatalf("ParseFaults unexpected: %v %v", faults, err)
	}
	if _, err := ParseFaults("bogus"); err == nil {
		t.Error("unknown fault must be rejected")
	}
}

func TestParseBuildValidation(t *testing.T) {
	if _, err := ParseBuild([]string{"--in", "a", "--out", "b",
		"--segment", "500ms", "--part", "500ms"}); err == nil {
		t.Error("segment shorter than part must be rejected")
	}
	if _, err := ParseBuild([]string{"--in", "a", "--out", "b",
		"--segment", "700ms", "--part", "500ms"}); err == nil {
		t.Error("segment not a multiple of part must be rejected")
	}
}
