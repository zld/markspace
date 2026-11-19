package report

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"streamcheck/internal/fixture"
	"streamcheck/internal/model"
	"streamcheck/internal/scan"
	"streamcheck/internal/verify"
)

func verification(t *testing.T) Verification {
	t.Helper()
	root := t.TempDir()
	plan := fixture.Plan{
		Root:     root,
		Rates:    []int{800000, 1600000, 3200000},
		Segments: 12,
		Drop:     map[int]int{1600000: 5},
		Shuffle:  map[int]bool{3200000: true},
	}
	if err := fixture.Generate(context.Background(), plan); err != nil {
		t.Fatalf("generate: %v", err)
	}
	scanned, err := scan.Scan(context.Background(), scan.Options{Root: root})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	res, err := verify.Run(context.Background(), root, scanned, verify.Options{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return NewVerification(res)
}

func TestTextAndJSONAgree(t *testing.T) {
	doc := verification(t)
	if doc.RateCount != 3 {
		t.Fatalf("rate_count = %d", doc.RateCount)
	}
	if doc.MissingCount != 1 {
		t.Fatalf("missing_count = %d", doc.MissingCount)
	}
	if doc.OutOfOrderCount == 0 {
		t.Fatal("out_of_order_count = 0")
	}
	if doc.DurationMS != 12000 {
		t.Fatalf("duration_ms = %d", doc.DurationMS)
	}
	if doc.Status != string(model.StatusFail) {
		t.Fatalf("status = %s", doc.Status)
	}

	var text bytes.Buffer
	if err := Text(&text, doc); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"rate_count=3",
		"segment_count=",
		"duration_ms=12000",
		"duration_deviation_ms=",
		"missing_count=1",
		"out_of_order_count=",
		"checksum_failures=",
	} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text output missing %q:\n%s", want, text.String())
		}
	}

	var jsonBuf bytes.Buffer
	if err := WriteJSON(&jsonBuf, doc); err != nil {
		t.Fatal(err)
	}
	var decoded Verification
	if err := json.Unmarshal(jsonBuf.Bytes(), &decoded); err != nil {
		t.Fatalf("json: %v", err)
	}
	if decoded.MissingCount != doc.MissingCount || decoded.SegmentCount != doc.SegmentCount ||
		decoded.DurationMS != doc.DurationMS || decoded.OutOfOrderCount != doc.OutOfOrderCount ||
		decoded.ChecksumFailures != doc.ChecksumFailures || decoded.RateCount != doc.RateCount {
		t.Fatalf("json and text summary diverged: %+v vs %+v", decoded, doc)
	}
	if len(decoded.Rates) != 3 {
		t.Fatalf("json rates = %d", len(decoded.Rates))
	}
}

func TestJSONSerializesEmptyIssuesAsArray(t *testing.T) {
	doc := Verification{Status: "pass", Issues: []model.Issue{}}
	var buf bytes.Buffer
	if err := WriteJSON(&buf, doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"issues": []`) {
		t.Fatalf("issues should be an array:\n%s", buf.String())
	}
}
