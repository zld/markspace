package fixture

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"mediasync/internal/container"
	"mediasync/internal/fault"
	"mediasync/internal/stream"
)

func writeAndParseMP4(t *testing.T, data []byte) (*container.File, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.mp4")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return container.Parse(context.Background(), p)
}

func writeAndReadStream(t *testing.T, data []byte) ([]stream.Record, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "p.bin")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return stream.ReadAll(context.Background(), p)
}

func TestScenariosDeterministic(t *testing.T) {
	first := BuildScenarios()
	second := BuildScenarios()
	if len(first) != 4 {
		t.Fatalf("want 4 scenarios, got %d", len(first))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("scenario order unstable at %d", i)
		}
		if !bytes.Equal(first[i].MP4, second[i].MP4) ||
			!bytes.Equal(first[i].Packets, second[i].Packets) {
			t.Fatalf("scenario %s is not byte-stable across runs", first[i].Name)
		}
	}
}

func TestOKFixtureParsesWithAlignedTracks(t *testing.T) {
	sc := BuildScenarios()[0]
	if sc.Name != NameOK {
		t.Fatalf("first scenario must be %s, got %s", NameOK, sc.Name)
	}
	f, err := writeAndParseMP4(t, sc.MP4)
	if err != nil {
		t.Fatalf("ok mp4 must parse: %v", err)
	}
	if len(f.Tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(f.Tracks))
	}
	dialogue, subtitle := f.Tracks[0], f.Tracks[1]
	if len(dialogue.Samples) != MP4SampleCount || len(subtitle.Samples) != MP4SampleCount {
		t.Fatal("sample count mismatch")
	}
	// Both tracks use the same 1kHz grid and identical spacing: zero drift.
	if dialogue.Marks[5].RawSeconds.Cmp(subtitle.Marks[5].RawSeconds) != 0 {
		t.Fatal("dialogue/subtitle timelines must coincide on the ok fixture")
	}

	records, err := writeAndReadStream(t, sc.Packets)
	if err != nil {
		t.Fatal(err)
	}
	packets, xorRecords, err := stream.ParseRTP(records)
	if err != nil {
		t.Fatal(err)
	}
	report, err := stream.Analyze(packets)
	if err != nil {
		t.Fatal(err)
	}
	if report.MissingCount() != 0 || len(xorRecords) != 0 {
		t.Fatal("ok packet stream must have no loss and no redundancy")
	}
}

func TestWrapDriftFixtureWrapsCleanly(t *testing.T) {
	sc := BuildScenarios()[1]
	if sc.Name != NameWrapDrift {
		t.Fatalf("want %s, got %s", NameWrapDrift, sc.Name)
	}
	records, err := writeAndReadStream(t, sc.Packets)
	if err != nil {
		t.Fatal(err)
	}
	packets, _, err := stream.ParseRTP(records)
	if err != nil {
		t.Fatal(err)
	}
	report, err := stream.Analyze(packets)
	if err != nil {
		t.Fatalf("clean wrap must unwrap without ambiguity: %v", err)
	}
	// Extended timestamps must be strictly increasing across the wrap.
	var prev int64
	for i, p := range report.Ordered {
		ts := report.ExtendedTS[p.Sequence]
		if i > 0 && ts <= prev {
			t.Fatalf("extended timestamp not monotonic at %d", i)
		}
		prev = ts
	}
}

func TestPacketLossFixtureRecoversOne(t *testing.T) {
	sc := BuildScenarios()[2]
	if sc.Name != NamePacketLoss {
		t.Fatalf("want %s, got %s", NamePacketLoss, sc.Name)
	}
	records, err := writeAndReadStream(t, sc.Packets)
	if err != nil {
		t.Fatal(err)
	}
	packets, xorRecords, err := stream.ParseRTP(records)
	if err != nil {
		t.Fatal(err)
	}
	report, err := stream.Analyze(packets)
	if err != nil {
		t.Fatal(err)
	}
	if report.MissingCount() != 1 {
		t.Fatalf("want exactly one gap, got %d", report.MissingCount())
	}
	frames, err := stream.ParseXOR(xorRecords)
	if err != nil {
		t.Fatal(err)
	}
	res, err := stream.Recover(report, frames)
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovered != 1 || res.Unrecovered != 0 {
		t.Fatalf("want 1 recovered/0 unrecovered, got %+v", res)
	}
}

func TestTruncatedFixtureFaultsWithOffset(t *testing.T) {
	sc := BuildScenarios()[3]
	if sc.Name != NameTruncated {
		t.Fatalf("want %s, got %s", NameTruncated, sc.Name)
	}
	_, err := writeAndParseMP4(t, sc.MP4)
	f, ok := fault.AsFault(err)
	if !ok {
		t.Fatalf("truncated fixture must fault, got %v", err)
	}
	if f.Code != fault.ErrTruncated || f.Offset < 0 {
		t.Fatalf("want attributed truncation, got %v", f)
	}
}
