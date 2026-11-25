package pipeline

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"replaycheck/internal/fixtures"
	"replaycheck/internal/ingest"
	"replaycheck/internal/model"
	"replaycheck/internal/policy"
	"replaycheck/internal/report"
)

type line struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

func collector() (*report.Writer, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return report.NewWriter(buf), buf
}

func decode(t *testing.T, buf *bytes.Buffer) []line {
	t.Helper()
	var out []line
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var l line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			t.Fatalf("bad json line: %v\n%s", err, raw)
		}
		out = append(out, l)
	}
	return out
}

func baseConfig(t *testing.T, kind fixtures.Kind) Config {
	dir := t.TempDir()
	return Config{
		FixtureDir: filepath.Join(dir, "fixtures"),
		SiteDir:    filepath.Join(dir, "site"),
		Kind:       kind,
		Budget:     policy.Budget{MaxBytes: 120_000, MaxProcMS: 5_000, MinQuality: 900_000},
		Limits:     ingest.DefaultLimits(),
	}
}

func TestNormalRunMeetsAcceptance(t *testing.T) {
	cfg := baseConfig(t, fixtures.KindNormal)
	cfg.Resume = true
	cfg.Verify = true
	out, buf := collector()
	res, err := Run(cfg, out)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, buf.String())
	}
	if res.Stages != 6 || res.Segments < 3 || !res.Valid {
		t.Fatalf("result %+v", res)
	}
	if res.MinQualityPPM < 900_000 {
		t.Fatalf("quality %d", res.MinQualityPPM)
	}
	var sawVerify bool
	for _, l := range decode(t, buf) {
		if l.Kind != report.KindVerify {
			continue
		}
		var rep struct {
			Valid       bool  `json:"valid"`
			TotalBytes  int64 `json:"totalBytes"`
			SummedBytes int64 `json:"summedBytes"`
		}
		json.Unmarshal(l.Payload, &rep)
		sawVerify = true
		if !rep.Valid || rep.TotalBytes != rep.SummedBytes || rep.TotalBytes != res.TotalBytes {
			t.Fatalf("verify line %+v", rep)
		}
	}
	if !sawVerify {
		t.Fatal("no verify line")
	}
}

func TestRerunHitsCacheAndDiffsZero(t *testing.T) {
	cfg := baseConfig(t, fixtures.KindNormal)
	out1, _ := collector()
	first, err := Run(cfg, out1)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Resume = true
	out2, buf2 := collector()
	second, err := Run(cfg, out2)
	if err != nil {
		t.Fatal(err)
	}
	if second.CacheHits == 0 || second.DiffCount != 0 {
		t.Fatalf("rerun hits=%d diff=%d", second.CacheHits, second.DiffCount)
	}
	if first.ManifestDigest != second.ManifestDigest {
		t.Fatal("manifest digest changed between identical runs")
	}
	for _, l := range decode(t, buf2) {
		if l.Kind == report.KindDiff {
			t.Fatalf("unexpected diff line: %s", l.Payload)
		}
	}
}

func TestResumeFromFailure(t *testing.T) {
	cfg := baseConfig(t, fixtures.KindNormal)
	cfg.Resume = true
	cfg.FailAt = model.StageManifest
	out, buf := collector()
	if _, err := Run(cfg, out); err == nil {
		t.Fatalf("injected failure did not surface\n%s", buf.String())
	}
	cfg.FailAt = ""
	cfg.Verify = true
	out2, buf2 := collector()
	res, err := Run(cfg, out2)
	if err != nil {
		t.Fatalf("resume: %v\n%s", err, buf2.String())
	}
	if res.ResumedFrom != "manifest" {
		t.Fatalf("resumedFrom=%q", res.ResumedFrom)
	}
	skipped := 0
	for _, l := range decode(t, buf2) {
		if l.Kind != report.KindStage {
			continue
		}
		var s struct {
			Resumed bool `json:"resumed"`
		}
		json.Unmarshal(l.Payload, &s)
		if s.Resumed {
			skipped++
		}
	}
	if skipped != 4 {
		t.Fatalf("expected 4 skipped stages on resume, got %d", skipped)
	}
	if !res.Valid || res.Segments < 3 {
		t.Fatalf("resumed result %+v", res)
	}
}

func TestTruncatedFixtureReportsIngestOffset(t *testing.T) {
	cfg := baseConfig(t, fixtures.KindTruncated)
	out, _ := collector()
	_, err := Run(cfg, out)
	if err == nil {
		t.Fatal("expected truncation failure")
	}
	me, ok := model.AsError(err)
	if !ok || me.Stage != model.StageIngest || me.Offset == 0 {
		t.Fatalf("bad error %v", err)
	}
	if !strings.Contains(me.Message, "truncated") {
		t.Fatalf("message %q", me.Message)
	}
}

func TestWrapAndTimebaseFixtures(t *testing.T) {
	for _, tc := range []struct {
		kind   fixtures.Kind
		wraps  uint64
		minRem int64
	}{
		{fixtures.KindWrap, 1, 1},
		{fixtures.KindTimebase, 0, 1},
	} {
		cfg := baseConfig(t, tc.kind)
		out, buf := collector()
		res, err := Run(cfg, out)
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.kind, err, buf.String())
		}
		if res.WrapCount != tc.wraps {
			t.Fatalf("%s wraps=%d", tc.kind, res.WrapCount)
		}
		if res.RemainderNanos < tc.minRem {
			t.Fatalf("%s remainder=%d", tc.kind, res.RemainderNanos)
		}
		if !res.Valid {
			t.Fatalf("%s manifest invalid", tc.kind)
		}
	}
}

func TestDuplicateFixtureRejectedAtSplit(t *testing.T) {
	cfg := baseConfig(t, fixtures.KindDuplicate)
	out, _ := collector()
	_, err := Run(cfg, out)
	me, ok := model.AsError(err)
	if !ok || me.Stage != model.StageSplit || me.Offset == 0 {
		t.Fatalf("expected split/offset error, got %v", err)
	}
}

func TestBudgetConflictProducesNoManifest(t *testing.T) {
	cfg := baseConfig(t, fixtures.KindNormal)
	cfg.Budget.MaxBytes = 1
	out, _ := collector()
	if _, err := Run(cfg, out); err == nil {
		t.Fatal("expected budget conflict")
	}
}
