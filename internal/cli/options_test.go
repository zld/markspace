package cli

import "testing"

func TestParseDemoDefaults(t *testing.T) {
	opts, err := Parse([]string{"demo"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Transport != "both" || opts.Seed != 17 || opts.JSON != false {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
	if got := opts.Transports(); len(got) != 2 || got[0] != "udp" || got[1] != "tcp" {
		t.Fatalf("transport order = %v, want [udp tcp]", got)
	}
}

func TestParseExplicit(t *testing.T) {
	opts, err := Parse([]string{"demo", "--transport", "tcp", "--seed", "42", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.Transport != "tcp" || opts.Seed != 42 || !opts.JSON {
		t.Fatalf("unexpected options: %+v", opts)
	}
}

func TestParseErrors(t *testing.T) {
	cases := [][]string{
		{},
		{"frobnicate"},
		{"demo", "--transport", "sctp"},
		{"demo", "--seed", "-3"},
		{"demo", "extra-positional"},
	}
	for _, argv := range cases {
		if _, err := Parse(argv); err == nil {
			t.Errorf("expected error for %v", argv)
		}
	}
}

func TestValidateTargetURL(t *testing.T) {
	good := []string{
		"rtsp://127.0.0.1:53011/demo",
		"rtsp://localhost/demo",
	}
	for _, u := range good {
		if err := ValidateTargetURL(u); err != nil {
			t.Errorf("expected acceptance for %q: %v", u, err)
		}
	}

	bad := []string{
		"rtsp://8.8.8.8/demo",
		"rtsp://127.0.0.1.evil.com/demo",
		"http://127.0.0.1/demo",
		"://bad-url",
	}
	for _, u := range bad {
		if err := ValidateTargetURL(u); err == nil {
			t.Errorf("expected rejection for %q", u)
		}
	}
}
