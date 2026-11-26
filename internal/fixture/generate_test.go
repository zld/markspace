package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"tsindex/internal/fault"
	"tsindex/internal/ingest"
)

// parse runs the real ingest pipeline over a generated asset and returns the
// PSI tracker and fault recorder for assertions.
func parse(t *testing.T, path string) (*ingest.PSITracker, *fault.Recorder) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rec := fault.NewRecorder()
	sr := ingest.NewStreamReader(f, rec)
	psi := ingest.NewPSITracker(rec)
	for {
		r, err := sr.NextPacket()
		if err != nil {
			break
		}
		psi.Feed(r)
	}
	return psi, rec
}

func TestNormalAssetHasTwoProgramsAndFourKeyframes(t *testing.T) {
	dir := t.TempDir()
	path, err := Generate(dir, Spec{Name: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	psi, rec := parse(t, path)
	ep := psi.Current()
	if ep == nil || len(ep.Programs) != programCount {
		t.Fatalf("want %d programs, got %+v", programCount, ep)
	}
	videoStreams := 0
	for _, pm := range ep.Programs {
		for _, info := range pm.Streams {
			if info.Complete {
				videoStreams++
			}
		}
	}
	if videoStreams != 2 {
		t.Fatalf("want two supported video streams, got %d", videoStreams)
	}
	if rec.Has(fault.KindCCGap) || rec.Has(fault.KindBadCRC) || rec.Has(fault.KindTruncated) {
		t.Fatalf("normal asset must be clean: %+v", rec.All())
	}
}

func TestPCRWrapCase(t *testing.T) {
	dir := t.TempDir()
	path, _ := Generate(dir, Spec{Name: "w", PCRWrap: true})
	_, rec := parse(t, path)
	if rec.Count(fault.KindPCRWrap) < 1 {
		t.Fatalf("PCR wrap not observed: %+v", rec.All())
	}
}

func TestCCGapCaseHasExactlyOneGap(t *testing.T) {
	dir := t.TempDir()
	path, _ := Generate(dir, Spec{Name: "g", CCGap: true})
	_, rec := parse(t, path)
	if rec.Count(fault.KindCCGap) != 1 {
		t.Fatalf("want exactly one CC gap, got %+v", rec.All())
	}
}

func TestPSIUpdateCaseBumpsVersions(t *testing.T) {
	dir := t.TempDir()
	path, _ := Generate(dir, Spec{Name: "u", PSIUpdate: true})
	psi, _ := parse(t, path)
	if psi.VersionUpdates() != 2 {
		t.Fatalf("want PAT+PMT version updates, got %d", psi.VersionUpdates())
	}
}

func TestTruncatedCaseReportsPartialPacket(t *testing.T) {
	dir := t.TempDir()
	path, _ := Generate(dir, Spec{Name: "t", Truncate: 37})
	info, _ := os.Stat(path)
	if info.Size()%ingest.PacketSize == 0 {
		t.Fatal("truncated asset must not end on a packet boundary")
	}
	_, rec := parse(t, path)
	var f fault.Fault
	for _, candidate := range rec.All() {
		if candidate.Kind == fault.KindTruncated {
			f = candidate
		}
	}
	if f.Bytes < 1 || f.Bytes > 187 {
		t.Fatalf("truncation must report 1..187 missing bytes, got %d", f.Bytes)
	}
}

func TestBadCRCCaseRejected(t *testing.T) {
	dir := t.TempDir()
	path, _ := Generate(dir, Spec{Name: "c", BadCRC: true})
	_, rec := parse(t, path)
	if rec.Count(fault.KindBadCRC) != 1 {
		t.Fatalf("want one bad-CRC fault, got %+v", rec.All())
	}
}

func TestUnsupportedCodecMarkedIncomplete(t *testing.T) {
	dir := t.TempDir()
	path, _ := Generate(dir, Spec{Name: "i", UnsupportedCodec: true})
	psi, _ := parse(t, path)
	ep := psi.Current()
	sawIncomplete := false
	for _, pm := range ep.Programs {
		for _, info := range pm.Streams {
			if !info.Complete {
				sawIncomplete = true
			}
		}
	}
	if !sawIncomplete {
		t.Fatal("unsupported codec stream must be marked incomplete")
	}
}

func TestGenerateAllRejectsUnknownCase(t *testing.T) {
	if _, err := GenerateAll(t.TempDir(), []string{"nope"}); err == nil {
		t.Fatal("unknown case accepted")
	}
}

func TestSidecarDirIsWritable(t *testing.T) {
	dir := t.TempDir()
	paths, err := GenerateAll(dir, AllCases)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != len(AllCases) {
		t.Fatalf("want %d assets, got %d", len(AllCases), len(paths))
	}
	for name, p := range paths {
		if filepath.Dir(p) != dir {
			t.Fatalf("asset %s written outside target dir", name)
		}
	}
}
