package mp4

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"testing"

	"regional.example/mediad/internal/fault"
	"regional.example/mediad/internal/fixtures"
)

func scanFixture(t *testing.T, dir string, sc fixtures.Scenario) (*ScanResult, fixtures.Generated) {
	t.Helper()
	gens, err := fixtures.Generate(dir, 41)
	if err != nil {
		t.Fatal(err)
	}
	var g fixtures.Generated
	for _, gg := range gens {
		if gg.Scenario == sc {
			g = gg
		}
	}
	res, err := (&Scanner{StartTick: 0}).Scan(g.MP4Path)
	if err != nil {
		t.Fatalf("%s: %v", sc, err)
	}
	return res, g
}

func TestScanNormal(t *testing.T) {
	res, g := scanFixture(t, t.TempDir(), fixtures.ScenarioNormal)
	if res.Truncated {
		t.Fatal("normal file flagged truncated")
	}
	if res.IndexMissing {
		t.Fatal("normal file flagged missing index")
	}
	if len(res.Tracks) != 1 {
		t.Fatalf("tracks %d", len(res.Tracks))
	}
	ti := res.Tracks[0]
	if ti.Channels != 2 || ti.Layout != "stereo" {
		t.Fatalf("layout %d/%s", ti.Channels, ti.Layout)
	}
	var unconfirmed int
	for _, s := range ti.Samples {
		if !s.Confirmed {
			unconfirmed++
		}
	}
	if unconfirmed != 0 {
		t.Fatalf("%d of %d samples unconfirmed on a clean file", unconfirmed, len(ti.Samples))
	}
	if err := res.Timeline.Validate(); err != nil {
		t.Fatal(err)
	}
	if res.Timeline.MissingDuration() != 0 {
		t.Fatalf("clean file has %d missing ticks", res.Timeline.MissingDuration())
	}
	if res.CorruptOffset != -1 {
		t.Fatalf("clean file reports corrupt offset %d", res.CorruptOffset)
	}
	// PCM is padded to whole 1024-frame samples, so 144000 frames become 141
	// samples and every one of them must be confirmed.
	want := (g.Frames + 1024 - 1) / 1024
	if got := len(ti.Samples); got != want {
		t.Fatalf("sample count %d, want %d", got, want)
	}
}

func TestScanTruncatedKeepsLastFullSample(t *testing.T) {
	res, g := scanFixture(t, t.TempDir(), fixtures.ScenarioTruncated)
	if !res.Truncated {
		t.Fatal("truncated file not flagged")
	}
	if res.CorruptOffset <= 0 {
		t.Fatalf("expected positive corrupt offset, got %d", res.CorruptOffset)
	}
	ti := res.Tracks[0]
	var confirmed, unconfirmed int
	for _, s := range ti.Samples {
		if s.Confirmed {
			confirmed++
		} else {
			unconfirmed++
		}
	}
	if confirmed == 0 {
		t.Fatal("no confirmed samples survived truncation")
	}
	if unconfirmed == 0 {
		t.Fatal("truncated tail not marked missing")
	}
	if res.Timeline.MissingDuration() == 0 {
		t.Fatal("timeline does not record missing time")
	}
	// Every confirmed sample must end strictly before the readable length.
	for _, s := range ti.Samples {
		if s.Confirmed && s.Offset+s.Size > res.Bytes {
			t.Fatalf("confirmed sample %d ends at %d past readable %d", s.Index, s.Offset+s.Size, res.Bytes)
		}
	}
	_ = g
}

func TestScanCorruptMarksDamage(t *testing.T) {
	res, _ := scanFixture(t, t.TempDir(), fixtures.ScenarioCorrupt)
	var unconfirmed int
	for _, ti := range res.Tracks {
		for _, s := range ti.Samples {
			if !s.Confirmed {
				unconfirmed++
			}
		}
	}
	if unconfirmed == 0 {
		t.Fatal("corrupt fixture produced no unconfirmed samples")
	}
	var codes []string
	for _, f := range res.Damaged {
		codes = append(codes, f.Code)
	}
	if !containsCode(codes, "unaligned-sample") && !containsCode(codes, "chunk-overlap") {
		t.Fatalf("expected damage codes, got %v", codes)
	}
	if res.CorruptOffset <= 0 {
		t.Fatalf("corrupt offset %d", res.CorruptOffset)
	}
	if res.Timeline == nil || res.Timeline.MissingDuration() == 0 {
		t.Fatal("corruption must surface as missing duration")
	}
}

func TestScanSparseRebuildsIndex(t *testing.T) {
	res, g := scanFixture(t, t.TempDir(), fixtures.ScenarioSparse)
	if !res.IndexMissing {
		t.Fatal("sparse fixture should report missing chunk index")
	}
	if res.Tracks[0].Layout != "mono" {
		t.Fatalf("layout %s", res.Tracks[0].Layout)
	}
	if len(res.Tracks[0].Edits) != 2 {
		t.Fatalf("edits %v", res.Tracks[0].Edits)
	}
	var confirmed int
	for _, s := range res.Tracks[0].Samples {
		// Rebuilt offsets start at mdat; with fast-start absent the first
		// chunk lives right after ftyp, so extents must be readable.
		if s.Confirmed {
			confirmed++
		}
	}
	if confirmed == 0 {
		t.Fatal("no offsets could be rebuilt")
	}
	if res.CorruptOffset >= 0 && res.Truncated {
		t.Fatal("sparse file is not truncated")
	}
	_ = g
}

func TestScanSparseNonZeroStart(t *testing.T) {
	dir := t.TempDir()
	gens, _ := fixtures.Generate(dir, 41)
	var path string
	for _, g := range gens {
		if g.Scenario == fixtures.ScenarioSparse {
			path = g.MP4Path
		}
	}
	res, err := (&Scanner{StartTick: 33075}).Scan(path)
	if err != nil {
		t.Fatal(err)
	}
	start, ok := res.Timeline.Start()
	if !ok || start != 33075 {
		t.Fatalf("timeline start %d ok=%v", start, ok)
	}
	segs := res.Timeline.Segments()
	if len(segs) == 0 || segs[0].Start != 33075 {
		t.Fatalf("first segment %+v", segs[0])
	}
}

func TestScanRejectsNonAudioCodec(t *testing.T) {
	// Hand-build a struct track that claims mp4a so buildTrack rejects it
	// before any media is read.
	_, _, err := buildTrack(structTrack{
		id: 9, handler: "soun", mediaTimescale: 48000, sampleType: "mp4a",
		channels: 2, sampleSizeBits: 16, stszConstant: 4, stszCount: 1,
	}, 1<<20, 100, 0)
	if err == nil {
		t.Fatal("expected codec rejection")
	}
	if !faultIs(err, fault.ClassAudio, "codec-unsupported") {
		t.Fatalf("got %v", err)
	}
}

func TestScanSampleCap(t *testing.T) {
	dir := t.TempDir()
	gens, _ := fixtures.Generate(dir, 41)
	path := gens[0].MP4Path
	_, err := (&Scanner{MaxSamples: 10}).Scan(path)
	if err == nil {
		t.Fatal("expected sample cap error")
	}
	if !faultIs(err, fault.ClassLimit, "sample-count") {
		t.Fatalf("got %v", err)
	}
}

func TestRecoveryDeterministic(t *testing.T) {
	dir := t.TempDir()
	gens, err := fixtures.Generate(dir, 41)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gens {
		if !g.ExpectTruncated && !g.ExpectIndexGap {
			continue
		}
		h1 := scanDigest(t, g.MP4Path)
		h2 := scanDigest(t, g.MP4Path)
		if h1 != h2 {
			t.Fatalf("%s: recovery digest changed between runs: %s vs %s", g.Scenario, h1, h2)
		}
	}
}

// scanDigest hashes the deterministic recovery summary of a file.
func scanDigest(t *testing.T, path string) string {
	t.Helper()
	res, err := (&Scanner{}).Scan(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, ti := range res.Tracks {
		for _, s := range ti.Samples {
			fmt.Fprintf(h, "%d:%d:%d:%v\n", s.Index, s.Offset, s.Size, s.Confirmed)
		}
	}
	fmt.Fprintf(h, "corrupt=%d,trunc=%v,index=%v\n", res.CorruptOffset, res.Truncated, res.IndexMissing)
	return hex.EncodeToString(h.Sum(nil))
}

func TestPayloadReaderBounds(t *testing.T) {
	dir := t.TempDir()
	res, _ := scanFixture(t, dir, fixtures.ScenarioNormal)
	ti := res.Tracks[0]
	f, err := openAt(ti, res.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	pr := NewPayloadReader(f, ti.Samples[0], 1024)
	buf := make([]byte, ti.Samples[0].Size)
	if err := pr.ReadFull(buf); err != nil {
		t.Fatal(err)
	}
	// Reading past the extent must report EOF, never read neighbouring data.
	if n, err := pr.Read(make([]byte, 16)); n != 0 || err != io.EOF {
		t.Fatalf("post-extent read n=%d err=%v", n, err)
	}
}

func containsCode(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

func faultIs(err error, class fault.Class, code string) bool {
	type m interface{ Error() string }
	_ = m(nil)
	return errorMatches(err, class, code)
}

func errorMatches(err error, class fault.Class, code string) bool {
	f, ok := err.(fault.Fault)
	return ok && f.Class == class && f.Code == code
}
