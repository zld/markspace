package demo

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"mediacheck/internal/fixture"
)

func TestRunAllProducesSixScenarios(t *testing.T) {
	results, err := NewRunner(7).RunAll()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		fixture.ScenarioNormal, fixture.ScenarioReorderLoss,
		fixture.ScenarioTruncatedNAL, fixture.ScenarioReplace,
		fixture.ScenarioMetadata, fixture.ScenarioCommitAbort,
	}
	if len(results) != len(want) {
		t.Fatalf("scenario count = %d, want %d", len(results), len(want))
	}
	for i, name := range want {
		if results[i].Name != name {
			t.Fatalf("scenario %d = %s, want %s", i, results[i].Name, name)
		}
		if results[i].Digest == "" || results[i].Manifest == nil || results[i].Report == nil {
			t.Fatalf("scenario %s missing report/manifest/digest", name)
		}
	}
}

func TestRenderContainsAllRequiredColumns(t *testing.T) {
	results, _ := NewRunner(7).RunAll()
	var buf bytes.Buffer
	Render(&buf, results)
	out := buf.String()
	for _, token := range []string{
		"input_seq", "bytes(offset:len)", "time(start..end,dur)",
		"key", "faults", "class=", "summary:",
	} {
		if !strings.Contains(out, token) {
			t.Fatalf("rendered report missing %q", token)
		}
	}
}

func TestStableSummaryHasNoPathsOrTimestamps(t *testing.T) {
	results, _ := NewRunner(7).RunAll()
	var buf bytes.Buffer
	StableSummary(&buf, results)
	out := buf.String()
	for _, leak := range []string{"/tmp", "/Users", ".go", "2026", "2025", "2024"} {
		if strings.Contains(out, leak) {
			t.Fatalf("stable summary leaks runtime path/time token %q", leak)
		}
	}
	if !strings.Contains(out, "sha256:manifest:") {
		t.Fatal("summary must carry stable manifest digests")
	}
}

func TestExitCodeSelection(t *testing.T) {
	results, _ := NewRunner(7).RunAll()
	// The full set includes both damage and a rolled-back migration, so
	// migration failure (3) takes precedence.
	if code := ExitCode(results); code != ExitMigrateFailed {
		t.Fatalf("combined exit code = %d, want %d", code, ExitMigrateFailed)
	}
}

func TestWriteMP4RoundTrip(t *testing.T) {
	results, _ := NewRunner(7).RunAll()
	buf := newTestSeeker()
	if err := WriteMP4(buf, results[0].Report); err != nil {
		t.Fatal(err)
	}
	if len(buf.Bytes()) == 0 {
		t.Fatal("MP4 output is empty")
	}
	if string(buf.Bytes()[4:8]) != "ftyp" {
		t.Fatalf("MP4 does not start with ftyp: %#x", buf.Bytes()[4:8])
	}
	first := results[0].Report.Samples[0]
	if len(first.NALUnits) == 0 || len(first.NALUnits[0]) == 0 {
		t.Fatal("MP4 sample source must retain reassembled NAL units")
	}
	serialized := lengthPrefixedNALs(first.NALUnits)
	prefix := binary.BigEndian.Uint32(serialized[:4])
	if int(prefix) != len(first.NALUnits[0]) {
		t.Fatalf("length-prefixed NAL prefix=%d, want %d", prefix, len(first.NALUnits[0]))
	}
}

type testSeeker struct {
	data []byte
	pos  int64
}

func newTestSeeker() *testSeeker { return &testSeeker{} }

func (b *testSeeker) Write(p []byte) (int, error) {
	end := b.pos + int64(len(p))
	if int(end) > len(b.data) {
		grown := make([]byte, end)
		copy(grown, b.data)
		b.data = grown
	}
	copy(b.data[b.pos:end], p)
	b.pos = end
	return len(p), nil
}

func (b *testSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case 0:
		b.pos = offset
	case 1:
		b.pos += offset
	case 2:
		b.pos = int64(len(b.data)) + offset
	}
	return b.pos, nil
}

func (b *testSeeker) Bytes() []byte { return b.data }
