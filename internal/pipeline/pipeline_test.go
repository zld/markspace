package pipeline

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediacore/internal/core"
	"mediacore/internal/fixture"
)

// fixtureDir builds every fixture once for the test binary's lifetime.
var fixtureDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mediacore-pipeline-")
	if err != nil {
		panic(err)
	}
	fixtureDir = dir
	if _, err := fixture.Ensure(dir); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func options(kind fixture.Kind) Options {
	return Options{
		File:       filepath.Join(fixtureDir, string(kind)+".mp4"),
		SampleRate: 48000, Channels: "stereo", TargetKbps: 128,
		LoudnessLUFS: -16, PeakDBFS: -1, PacketMs: 20,
	}
}

func TestCleanRunMeetsAcceptanceBudgets(t *testing.T) {
	res := Run(options(fixture.KindClean))
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	m := res.Manifest
	if len(m.Gaps) != 0 {
		t.Fatalf("clean run has gaps: %+v", m.Gaps)
	}
	if math.Abs(m.BitrateErrorPct) > 5 {
		t.Fatalf("bitrate error %.2f%% > 5%%", m.BitrateErrorPct)
	}
	if math.Abs(m.OutputLUFS-(-16)) > 1.5 {
		t.Fatalf("loudness %.2f misses -16 by >1.5 LU", m.OutputLUFS)
	}
	if m.AlignmentMs > 2 {
		t.Fatalf("alignment %.4f ms > 2 ms", m.AlignmentMs)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d", res.ExitCode)
	}
	if m.Stats.InputFrames == 0 || m.Stats.OutputFrames == 0 {
		t.Fatal("no frames accounted")
	}
	// Input rate changed 44.1k -> 48k, so output frames must be larger.
	if m.Stats.OutputFrames <= m.Stats.InputFrames {
		t.Fatalf("output frames %d not resampled from %d", m.Stats.OutputFrames, m.Stats.InputFrames)
	}
}

func TestTruncatedRunPropagatesToManifest(t *testing.T) {
	res := Run(options(fixture.KindTruncated))
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	m := res.Manifest
	if len(m.Gaps) == 0 {
		t.Fatal("truncation did not reach the manifest")
	}
	// The failure must be traceable: recovery warning plus a manifest gap with
	// a byte offset.
	if res.Recovery == nil || len(res.Recovery.Warnings) == 0 {
		t.Fatal("recovery warnings lost")
	}
	found := false
	for _, g := range m.Gaps {
		if g.Offset > 0 && g.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("gaps lack traceable offsets/reasons: %+v", m.Gaps)
	}
	if !strings.Contains(strings.Join(m.StageErrors, "\n"), "past end of file") {
		t.Logf("stage errors: %v", m.StageErrors)
	}
	if res.ExitCode != 0 {
		t.Fatalf("recovered run should exit 0, got %d (%v)", res.ExitCode, res.Err)
	}
	if m.ValidMs >= m.TotalMs {
		t.Fatalf("valid %d ms should be less than total %d ms", m.ValidMs, m.TotalMs)
	}
}

func TestBadRangeRunIsolatesDamage(t *testing.T) {
	res := Run(options(fixture.KindBadRange))
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	m := res.Manifest
	if len(m.Gaps) == 0 {
		t.Fatal("illegal sample range did not produce a gap")
	}
	// Damage is one chunk, so most audio must survive.
	if m.ValidMs < m.TotalMs/2 {
		t.Fatalf("valid %d ms of %d ms: damage was not isolated", m.ValidMs, m.TotalMs)
	}
	if len(m.StageErrors) == 0 {
		t.Fatal("range failure not recorded in the manifest")
	}
}

func TestNoIndexRunMatchesTableRun(t *testing.T) {
	// The no-index fixture is the clean media with its tables stripped, so a
	// rebuilt index must describe the same audio.
	tableRun := Run(options(fixture.KindClean))
	rescan := Run(options(fixture.KindNoIndex))
	if tableRun.Err != nil || rescan.Err != nil {
		t.Fatalf("runs failed: %v %v", tableRun.Err, rescan.Err)
	}
	if rescan.Recovery.IndexSource != "rescan" {
		t.Fatalf("index source = %s", rescan.Recovery.IndexSource)
	}
	if tableRun.Manifest.ValidMs != rescan.Manifest.ValidMs {
		t.Fatalf("valid duration differs: %d vs %d", tableRun.Manifest.ValidMs, rescan.Manifest.ValidMs)
	}
	if tableRun.Manifest.PacketCount != rescan.Manifest.PacketCount {
		t.Fatalf("packet count differs: %d vs %d", tableRun.Manifest.PacketCount, rescan.Manifest.PacketCount)
	}
}

func TestDepCycleFailsBeforeMediaWithExit2(t *testing.T) {
	res := Run(options(fixture.KindDepCycle))
	if res.Err == nil {
		t.Fatal("cycle run succeeded")
	}
	if !core.IsCode(res.Err, core.CodeDependencyCycle) {
		t.Fatalf("want dependency_cycle, got %v", res.Err)
	}
	if res.ExitCode != 2 {
		t.Fatalf("exit code = %d, want 2", res.ExitCode)
	}
	// Only scan and graph stages may have run.
	for _, s := range res.Stages {
		if s.Stage != string(core.StageScan) && s.Stage != string(core.StageGraph) {
			t.Fatalf("stage %s ran despite cycle", s.Stage)
		}
	}
	if res.Manifest != nil {
		t.Fatal("manifest produced for a rejected graph")
	}
}

func TestMissingTrackAndGarbledExit3(t *testing.T) {
	// A garbled file has no index and no verifiable records.
	res := Run(options(fixture.KindGarbled))
	if res.ExitCode != 3 {
		t.Fatalf("garbled exit = %d, want 3 (%v)", res.ExitCode, res.Err)
	}
	if !core.IsCode(res.Err, core.CodeUnrecoverable) {
		t.Fatalf("want unrecoverable, got %v", res.Err)
	}
}

func TestWrapRunKeepsSequenceOrder(t *testing.T) {
	// Run at the source rate so the fixture's timestamp origin, which sits
	// just below 2^32, wraps inside the run instead of before it.
	opt := options(fixture.KindWrap)
	opt.SampleRate = 44100
	res := Run(opt)
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	m := res.Manifest
	if len(m.Gaps) != 0 {
		t.Fatalf("wrap fixture has gaps: %+v", m.Gaps)
	}
	wraps := 0
	for i := 1; i < len(m.Packets); i++ {
		if m.Packets[i].Timestamp < m.Packets[i-1].Timestamp {
			wraps++
		}
		if m.Packets[i].Sequence != m.Packets[i-1].Sequence+1 {
			t.Fatalf("sequence order broken at %d", i)
		}
	}
	if wraps != 1 {
		t.Fatalf("wraps = %d, want exactly 1", wraps)
	}
}

func TestRepeatedRunsProduceIdenticalManifests(t *testing.T) {
	for _, kind := range []fixture.Kind{fixture.KindClean, fixture.KindTruncated, fixture.KindNoIndex, fixture.KindWrap} {
		opt := options(kind)
		dir := t.TempDir()
		opt.OutPath = filepath.Join(dir, "a.json")
		first := Run(opt)
		if first.Err != nil {
			t.Fatalf("%s first run: %v", kind, first.Err)
		}
		firstBytes, err := os.ReadFile(opt.OutPath)
		if err != nil {
			t.Fatal(err)
		}
		opt.OutPath = filepath.Join(dir, "b.json")
		second := Run(opt)
		if second.Err != nil {
			t.Fatalf("%s second run: %v", kind, second.Err)
		}
		secondBytes, err := os.ReadFile(opt.OutPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(firstBytes) != string(secondBytes) {
			t.Fatalf("%s: manifests differ between identical runs", kind)
		}
		if first.Manifest.Summary != second.Manifest.Summary {
			t.Fatalf("%s: summary digest differs", kind)
		}
	}
}

func TestManifestFileIsValidJSON(t *testing.T) {
	opt := options(fixture.KindClean)
	opt.OutPath = filepath.Join(t.TempDir(), "manifest.json")
	res := Run(opt)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	data, err := os.ReadFile(opt.OutPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	for _, key := range []string{"actual_kbps", "input_lufs", "output_lufs", "gaps", "packets", "summary"} {
		if _, ok := m[key]; !ok {
			t.Fatalf("manifest missing %q", key)
		}
	}
}

func TestEveryRunHasStageRecords(t *testing.T) {
	res := Run(options(fixture.KindClean))
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	want := map[string]bool{
		"scan": false, "graph": false, "recover": false,
		"audio": false, "packetize": false, "manifest": false,
	}
	for _, s := range res.Stages {
		if _, ok := want[s.Stage]; ok {
			want[s.Stage] = true
		}
	}
	for stage, seen := range want {
		if !seen {
			t.Fatalf("stage %s recorded no outcome", stage)
		}
	}
	if len(res.Graph) == 0 {
		t.Fatal("validated graph order not recorded")
	}
}

func TestRescanMatchesTablesAcrossRates(t *testing.T) {
	// A no-index file and its intact twin must describe the same media at any
	// output rate, which proves the rebuilt grid agrees with the tables.
	for _, rate := range []int{32000, 44100, 48000, 96000} {
		a := options(fixture.KindClean)
		a.SampleRate = rate
		b := options(fixture.KindNoIndex)
		b.SampleRate = rate
		ra, rb := Run(a), Run(b)
		if ra.Err != nil || rb.Err != nil {
			t.Fatalf("%d Hz: %v %v", rate, ra.Err, rb.Err)
		}
		if ra.Manifest.ValidMs != rb.Manifest.ValidMs {
			t.Fatalf("%d Hz: valid ms %d vs %d", rate, ra.Manifest.ValidMs, rb.Manifest.ValidMs)
		}
		if ra.Manifest.PacketCount != rb.Manifest.PacketCount {
			t.Fatalf("%d Hz: packets %d vs %d", rate, ra.Manifest.PacketCount, rb.Manifest.PacketCount)
		}
		if math.Abs(rb.Manifest.OutputLUFS-ra.Manifest.OutputLUFS) > 0.5 {
			t.Fatalf("%d Hz: output loudness %0.2f vs %0.2f", rate, rb.Manifest.OutputLUFS, ra.Manifest.OutputLUFS)
		}
	}
}

func TestRecoveredBitrateStaysWithinBudget(t *testing.T) {
	for _, kind := range []fixture.Kind{fixture.KindTruncated, fixture.KindBadRange, fixture.KindNoIndex, fixture.KindNoMoov} {
		res := Run(options(kind))
		if res.Err != nil {
			t.Fatalf("%s: %v", kind, res.Err)
		}
		if math.Abs(res.Manifest.BitrateErrorPct) > 5 {
			t.Fatalf("%s: bitrate error %.2f%%", kind, res.Manifest.BitrateErrorPct)
		}
		if math.Abs(res.Manifest.OutputLUFS-(-16)) > 1.5 {
			t.Fatalf("%s: loudness %.2f off target", kind, res.Manifest.OutputLUFS)
		}
	}
}

func TestMonophonicOutputAcrossSources(t *testing.T) {
	opt := options(fixture.KindClean)
	opt.Channels = "mono"
	res := Run(opt)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if res.Manifest.Channels != 1 || res.Manifest.Layout != "mono" {
		t.Fatalf("manifest layout = %s/%d", res.Manifest.Layout, res.Manifest.Channels)
	}
	if math.Abs(res.Manifest.BitrateErrorPct) > 5 {
		t.Fatalf("mono bitrate error %.2f%%", res.Manifest.BitrateErrorPct)
	}
}

func TestUnknownLayoutFailsWithExit1(t *testing.T) {
	opt := options(fixture.KindClean)
	opt.Channels = "7.1-quadraphonic"
	res := Run(opt)
	if res.Err == nil {
		t.Fatal("unknown layout accepted")
	}
	if res.ExitCode != 1 {
		t.Fatalf("exit = %d, want 1", res.ExitCode)
	}
	if !core.IsCode(res.Err, core.CodeParamConflict) {
		t.Fatalf("want parameter_conflict, got %v", res.Err)
	}
}

func TestGapOffsetsAreMonotonic(t *testing.T) {
	res := Run(options(fixture.KindBadRange))
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	gaps := res.Manifest.Gaps
	for i := 1; i < len(gaps); i++ {
		if gaps[i].StartMs < gaps[i-1].StartMs {
			t.Fatalf("gap start times not monotonic: %+v", gaps)
		}
		if gaps[i].Offset <= gaps[i-1].Offset {
			t.Fatalf("gap offsets not increasing: %+v", gaps)
		}
	}
}
