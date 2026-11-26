package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func decodeReports(t *testing.T, buf *bytes.Buffer) []Report {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	var out []Report
	for dec.More() {
		var r Report
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode report: %v\n%s", err, buf.String())
		}
		out = append(out, r)
	}
	return out
}

func TestRunAllMeetsQuantifiedAcceptance(t *testing.T) {
	out := filepath.Join(t.TempDir(), "media-check")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--case", "all", "--out", out}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	rs := decodeReports(t, &stdout)
	if len(rs) != 4 {
		t.Fatalf("got %d reports: %s", len(rs), stdout.String())
	}
	byName := map[string]Report{}
	for _, r := range rs {
		byName[r.Case] = r
		if !r.OK {
			t.Fatalf("%s not OK: %s", r.Case, r.Error)
		}
	}
	for _, name := range []string{"normal", "recovery"} {
		r := byName[name]
		if math.Abs(r.DeltaPct) > 0.1 || r.SNRDB < 90 || r.AlignErrSamples > 1 {
			t.Fatalf("%s thresholds failed: %+v", name, r)
		}
		if r.SourceSHA == "" || r.Fragments == 0 {
			t.Fatalf("%s missing source/fragments: %+v", name, r)
		}
	}
	d := byName["damage"]
	if d.BadOffset < 0 || d.GapCount < 1 || d.Fragments != 0 {
		t.Fatalf("damage report failed: %+v", d)
	}
	rp := byName["repeat"]
	if rp.Invalidated != 1 || rp.CacheHit != 1 || rp.SourceSHA == "" {
		t.Fatalf("repeat report failed: %+v", rp)
	}
}

func TestUsageAndExternalInputExitCodes(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"--unknown"}, &out, &errBuf); code != 2 {
		t.Fatalf("unknown flag code=%d want2", code)
	}
	if code := run([]string{"--case", "nope"}, &out, &errBuf); code != 2 {
		t.Fatalf("bad case code=%d want2", code)
	}

	bad := filepath.Join(t.TempDir(), "foreign.wav")
	if err := os.WriteFile(bad, []byte("not RIFF data"), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errBuf.Reset()
	code := run([]string{"--input", bad}, &out, &errBuf)
	if code != 3 {
		t.Fatalf("external bad code=%d want3 stderr=%s", code, errBuf.String())
	}
	rs := decodeReports(t, &out)
	if len(rs) != 1 || rs[0].OK || rs[0].BadOffset != 0 {
		t.Fatalf("external report=%+v", rs)
	}
}

func TestExternalValidInputExitZero(t *testing.T) {
	f := genTone(toneSpec{SampleRate: 48000, Channels: 1, Frames: 256, Frequency: 440})
	path := filepath.Join(t.TempDir(), "valid.wav")
	if err := os.WriteFile(path, f.WAV, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	code := run([]string{"--input", path}, &stdout, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("valid input code=%d output=%s", code, stdout.String())
	}
	rs := decodeReports(t, &stdout)
	if len(rs) != 1 || !rs[0].OK {
		t.Fatalf("valid report=%+v", rs)
	}
}

func TestRepeatStableAcrossTwoInvocations(t *testing.T) {
	out := filepath.Join(t.TempDir(), "stable")
	var first, second bytes.Buffer
	if code := run([]string{"--case", "repeat", "--out", out}, &first, &bytes.Buffer{}); code != 0 {
		t.Fatalf("first repeat code=%d", code)
	}
	if code := run([]string{"--case", "repeat", "--out", out}, &second, &bytes.Buffer{}); code != 0 {
		t.Fatalf("second repeat code=%d", code)
	}
	r1 := decodeReports(t, &first)[0]
	r2 := decodeReports(t, &second)[0]
	if r1.SourceSHA != r2.SourceSHA {
		t.Fatalf("source SHA changed: %s vs %s", r1.SourceSHA, r2.SourceSHA)
	}
	if r2.CacheHit != 1 || r2.Invalidated != 1 {
		t.Fatalf("second repeat cache fields: hit=%d invalidated=%d", r2.CacheHit, r2.Invalidated)
	}
}
