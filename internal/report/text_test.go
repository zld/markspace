package report_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"bbdiff/internal/analyze"
	"bbdiff/internal/elfscan"
	"bbdiff/internal/report"
)

func sampleResult() *analyze.Result {
	return &analyze.Result{
		Summary: analyze.Summary{Added: 1, Removed: 1, Changed: 2, Uncertain: 0},
		Blocks: []analyze.Diff{
			{Address: "0x401020", InstructionCount: 5, SHA256: strings.Repeat("a", 64), Function: "audit_gate", Kind: "added"},
			{Address: "0x401038", InstructionCount: 2, SHA256: strings.Repeat("b", 64), Function: "verify", Kind: "changed", OldAddress: "0x401028", NewAddress: "0x401038", ChangedInstructions: 2},
			{Address: "0x401060", InstructionCount: 5, SHA256: strings.Repeat("c", 64), Function: "legacy_path", Kind: "removed"},
		},
		Functions: []analyze.FunctionReport{
			{Name: "verify", OldAddress: "0x401020", NewAddress: "0x401030", Status: "aligned", OldBlocks: 7, NewBlocks: 7, Changed: 2},
		},
		Diagnostics: []elfscan.Diagnostic{
			{Severity: elfscan.SeverityWarning, Code: "test-diag", Message: "something to note"},
		},
		OldPath: "old.elf",
		NewPath: "new.elf",
	}
}

func TestWriteJSONShape(t *testing.T) {
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, sampleResult()); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	summary, ok := doc["summary"].(map[string]any)
	if !ok {
		t.Fatal("missing summary object")
	}
	for _, key := range []string{"added", "removed", "changed", "uncertain"} {
		if _, ok := summary[key]; !ok {
			t.Errorf("summary missing %q", key)
		}
	}
	blocks, ok := doc["blocks"].([]any)
	if !ok {
		t.Fatal("missing blocks array")
	}
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}
	first, _ := blocks[0].(map[string]any)
	for _, key := range []string{"address", "instruction_count", "sha256", "function", "kind"} {
		if _, ok := first[key]; !ok {
			t.Errorf("block missing %q", key)
		}
	}
}

func TestWriteJSONEmptyBlocksIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, &analyze.Result{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\"blocks\": []") {
		t.Fatalf("empty blocks are not rendered as []:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "\"summary\"") {
		t.Fatalf("missing summary:\n%s", buf.String())
	}
}

func TestWriteJSONNilResult(t *testing.T) {
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, nil); err != nil {
		t.Fatalf("WriteJSON(nil): %v", err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Fatalf("nil result produced invalid JSON: %s", buf.String())
	}
}

func TestWriteJSONDeterministic(t *testing.T) {
	var a, b bytes.Buffer
	res := sampleResult()
	if err := report.WriteJSON(&a, res); err != nil {
		t.Fatal(err)
	}
	if err := report.WriteJSON(&b, res); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("two renders differ")
	}
}

func TestWriteText(t *testing.T) {
	var buf bytes.Buffer
	if err := report.WriteText(&buf, sampleResult(), report.DefaultOptions()); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"summary: added=1 removed=1 changed=2 uncertain=0", "0x401020", "verify", "test-diag"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteTextHidesDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	opt := report.DefaultOptions()
	opt.ShowDiagnostics = false
	if err := report.WriteText(&buf, sampleResult(), opt); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "test-diag") {
		t.Fatal("diagnostics were printed despite ShowDiagnostics=false")
	}
}

func TestWriteTextMaxBlocks(t *testing.T) {
	res := sampleResult()
	var buf bytes.Buffer
	opt := report.DefaultOptions()
	opt.MaxBlocks = 1
	if err := report.WriteText(&buf, res, opt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "more blocks omitted") {
		t.Fatalf("truncation notice missing:\n%s", buf.String())
	}
}

func TestWriteTextCancelled(t *testing.T) {
	res := sampleResult()
	res.Cancelled = true
	res.Failures = []string{"old image: function \"x\": boom"}
	var buf bytes.Buffer
	if err := report.WriteText(&buf, res, report.DefaultOptions()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "cancelled") {
		t.Fatal("cancelled run not flagged")
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatal("failure not reported")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteTextPropagatesWriteErrors(t *testing.T) {
	err := report.WriteText(failWriter{}, sampleResult(), report.DefaultOptions())
	if err == nil {
		t.Fatal("WriteText swallowed a write error")
	}
}

func TestWriteJSONPropagatesWriteErrors(t *testing.T) {
	err := report.WriteJSON(failWriter{}, sampleResult())
	if err == nil {
		t.Fatal("WriteJSON swallowed a write error")
	}
}
