package main

import "testing"

func TestParseFlags(t *testing.T) {
	allowed := map[string]bool{"input": true, "resume": false, "queue": true}
	fl, err := parseFlags([]string{"--input", "generated", "--resume", "--queue", "1"}, allowed)
	if err != nil {
		t.Fatal(err)
	}
	if fl.values["input"] != "generated" || !fl.bools["resume"] {
		t.Fatalf("parsed flags wrong: %+v", fl)
	}
	n, err := fl.intOr("queue", 2)
	if err != nil || n != 1 {
		t.Fatalf("queue parse: %d %v", n, err)
	}
	if n, err := fl.intOr("missing", 7); err != nil || n != 7 {
		t.Fatal("default value not applied")
	}
}

func TestParseFlagsRejectsUnknown(t *testing.T) {
	if _, err := parseFlags([]string{"--bogus"}, map[string]bool{}); err == nil {
		t.Fatal("unknown flag must be rejected")
	}
	if _, err := parseFlags([]string{"--input"}, map[string]bool{"input": true}); err == nil {
		t.Fatal("flag without value must be rejected")
	}
	if _, err := parseFlags([]string{"positional"}, map[string]bool{}); err == nil {
		t.Fatal("positional arguments must be rejected")
	}
}

func TestValidCaseAndStage(t *testing.T) {
	for _, c := range []string{"normal", "truncated-box", "tfdt-regression", "mfhd-gap",
		"duplicate-conflict", "duplicate-fold"} {
		if !validCase(caseType(c)) {
			t.Fatalf("case %s should be valid", c)
		}
	}
	if validCase("nope") {
		t.Fatal("unknown case must be invalid")
	}
	for _, s := range []string{"scan", "align", "manifest", "complete"} {
		if !validStage(stageType(s)) {
			t.Fatalf("stage %s should be valid", s)
		}
	}
}

func TestRunDispatch(t *testing.T) {
	if code := run(nil); code != exitInvalid {
		t.Fatalf("no args exit = %d, want 2", code)
	}
	if code := run([]string{"frobnicate"}); code != exitInvalid {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	if code := run([]string{"help"}); code != exitOK {
		t.Fatalf("help exit = %d, want 0", code)
	}
	if code := run([]string{"demo", "--representations", "1"}); code != exitInvalid {
		t.Fatalf("bad demo params exit = %d, want 2", code)
	}
	if code := run([]string{"verify"}); code != exitInvalid {
		t.Fatalf("verify without manifest exit = %d, want 2", code)
	}
}
