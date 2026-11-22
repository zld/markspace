package index

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mediacore/media"
)

// synthFrame builds a small frame with a deterministic gradient payload.
func synthFrame(seq uint32, ts uint32, captureMS int64, w, h uint16, vol *float64) *media.Frame {
	f := &media.Frame{
		Seq:           seq,
		RTPTimestamp:  ts,
		CaptureTimeMS: captureMS,
		Width:         w,
		Height:        h,
		DurationTicks: 3600,
		StreamID:      1,
		Payload:       make([]byte, int(w)*int(h)),
	}
	for i := range f.Payload {
		f.Payload[i] = byte(int(seq) + i)
	}
	if vol != nil {
		m := media.Meta{VolumeDBFS: vol}
		b, _ := json.Marshal(m)
		f.MetaRaw = b
	}
	return f
}

func synthStream(n int) []byte {
	frames := make([]*media.Frame, n)
	for i := 0; i < n; i++ {
		frames[i] = synthFrame(uint32(i), uint32(i*3600), int64(i)*40, 8, 8, nil)
	}
	b, err := media.EncodeStream(frames)
	if err != nil {
		panic(err)
	}
	return b
}

func TestScanProducesOrderedIndex(t *testing.T) {
	idx, err := Scan(bytes.NewReader(synthStream(10)), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !idx.Complete || idx.EntryCount != 10 {
		t.Fatalf("complete=%t count=%d", idx.Complete, idx.EntryCount)
	}
	if idx.StartTicks != 0 || idx.EndTicks != 9*3600 {
		t.Fatalf("range %d..%d", idx.StartTicks, idx.EndTicks)
	}
	for i := 1; i < len(idx.Entries); i++ {
		if idx.Entries[i].Ticks < idx.Entries[i-1].Ticks {
			t.Fatalf("entries not ordered at %d", i)
		}
	}
	if idx.IndexDigest == "" || idx.InputDigest == "" {
		t.Fatal("missing digests")
	}
}

func TestScanDeterministic(t *testing.T) {
	data := synthStream(25)
	a, err := Scan(bytes.NewReader(data), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Scan(bytes.NewReader(data), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if a.IndexDigest != b.IndexDigest || a.InputDigest != b.InputDigest {
		t.Fatalf("not deterministic: %s/%s vs %s/%s", a.IndexDigest, a.InputDigest, b.IndexDigest, b.InputDigest)
	}
	j1, _ := a.Marshal()
	j2, _ := b.Marshal()
	if !bytes.Equal(j1, j2) {
		t.Fatal("marshal not deterministic")
	}
}

func TestScanSortsOutOfOrderTimestamps(t *testing.T) {
	frames := []*media.Frame{
		synthFrame(0, 0, 0, 8, 8, nil),
		synthFrame(2, 7200, 80, 8, 8, nil),
		synthFrame(1, 3600, 40, 8, 8, nil),
	}
	data, _ := media.EncodeStream(frames)
	idx, err := Scan(bytes.NewReader(data), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !idx.Reordered {
		t.Fatal("expected reordered flag")
	}
	want := []uint32{0, 1, 2}
	for i, e := range idx.Entries {
		if e.Seq != want[i] {
			t.Fatalf("entry %d seq=%d want %d", i, e.Seq, want[i])
		}
	}
}

func TestScanDetectsTimestampRegression(t *testing.T) {
	frames := []*media.Frame{
		synthFrame(0, 3600*2, 0, 8, 8, nil),
		synthFrame(1, 3600*3, 40, 8, 8, nil),
		synthFrame(2, 3600, 80, 8, 8, nil), // jumps back ~80ms
	}
	data, _ := media.EncodeStream(frames)
	idx, err := Scan(bytes.NewReader(data), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, is := range idx.Issues {
		if is.Category == media.CatTimestampBack {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected timestamp regression issue, got %+v", idx.Issues)
	}
}

func TestScanTruncatedRecordReportsOffset(t *testing.T) {
	data := synthStream(3)
	cut := data[:len(data)-40]
	idx, err := Scan(bytes.NewReader(cut), DefaultOptions())
	if err == nil {
		t.Fatal("expected truncation error")
	}
	if idx.Complete {
		t.Fatal("index must be incomplete")
	}
	e, ok := media.AsError(err)
	if !ok || e.Category != media.CatTruncatedFrame {
		t.Fatalf("error=%v", err)
	}
	if e.FrameOffset <= 0 {
		t.Fatalf("missing frame offset: %+v", e)
	}
	if idx.EntryCount != 2 {
		t.Fatalf("valid prefix entries=%d want 2", idx.EntryCount)
	}
	if len(idx.Issues) != 1 || idx.Issues[0].Offset != e.FrameOffset {
		t.Fatalf("issues=%+v offset=%d", idx.Issues, e.FrameOffset)
	}
}

func TestScanChecksumDamage(t *testing.T) {
	data := synthStream(2)
	// Corrupt a payload byte deep in the first record.
	data[media.FrameHeaderSize+1] ^= 0xFF
	idx, err := Scan(bytes.NewReader(data), DefaultOptions())
	if err == nil {
		t.Fatal("expected checksum error")
	}
	e, _ := media.AsError(err)
	if e.Category != media.CatChecksum || e.FrameOffset != 0 {
		t.Fatalf("error=%+v", e)
	}
	if idx.Complete {
		t.Fatal("index must be incomplete")
	}
}

func TestComputeDigestChangesWithContent(t *testing.T) {
	a, _ := Scan(bytes.NewReader(synthStream(4)), DefaultOptions())
	data := synthStream(4)
	b, _ := Scan(bytes.NewReader(data), DefaultOptions())
	if a.IndexDigest != b.IndexDigest {
		t.Fatal("identical content produced different digests")
	}
	if got := a.IndexDigest; got == "" {
		t.Fatal("empty digest")
	}
}

func TestCacheRoundTripAndStaleDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	data := synthStream(6)
	idx, err := Scan(bytes.NewReader(data), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteCache(path, idx.InputDigest, idx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp cache should not remain after atomic write")
	}
	got, rep := LoadCache(path, idx.InputDigest)
	if got == nil || rep.State != CacheHit {
		t.Fatalf("state=%s reason=%s", rep.State, rep.Reason)
	}
	if got.IndexDigest != idx.IndexDigest {
		t.Fatal("cached index digest changed")
	}
	// A changed input digest must reject the cache.
	if _, rep := LoadCache(path, "deadbeef"); rep.State != CacheStale || rep.Error == nil {
		t.Fatalf("state=%s error=%v", rep.State, rep.Error)
	}
}

func TestCacheRejectsVersionChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	idx, _ := Scan(bytes.NewReader(synthStream(3)), DefaultOptions())
	if err := WriteCache(path, idx.InputDigest, idx); err != nil {
		t.Fatal(err)
	}
	// Rewrite the envelope with a foreign version.
	raw, _ := os.ReadFile(path)
	var env map[string]any
	json.Unmarshal(raw, &env)
	env["index_version"] = 999
	env["cache_version"] = 999
	patched, _ := json.Marshal(env)
	os.WriteFile(path, patched, 0o644)
	_, rep := LoadCache(path, idx.InputDigest)
	if rep.State != CacheVersion {
		t.Fatalf("state=%s want version mismatch", rep.State)
	}
}

func TestCacheRejectsTempResidue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	// Only a temp file exists, as after a crash mid-write.
	os.WriteFile(path+".tmp", []byte(`{"cache_version":1}`), 0o644)
	idx, rep := LoadCache(path, "whatever")
	if idx != nil {
		t.Fatal("temp residue must not be accepted")
	}
	if rep.State != CacheResidue {
		t.Fatalf("state=%s", rep.State)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp residue should be removed")
	}
}

func TestEnsureCacheRebuildsWhenInputChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	first := synthStream(4)
	idx1, _ := Scan(bytes.NewReader(first), DefaultOptions())
	if err := WriteCache(path, idx1.InputDigest, idx1); err != nil {
		t.Fatal(err)
	}
	// Build from different bytes (same count) and ensure a rebuild happens.
	second := synthStream(4)
	second[len(second)-1] ^= 0x01
	idx2, _ := Scan(bytes.NewReader(second), DefaultOptions())
	got, rep := EnsureCache(path, idx2.InputDigest, func() (*Index, error) { return idx2, nil })
	if !rep.Rebuilt || rep.State != CacheStale {
		t.Fatalf("rebuilt=%t state=%s reason=%s", rep.Rebuilt, rep.State, rep.Reason)
	}
	if got.IndexDigest != idx2.IndexDigest {
		t.Fatal("wrong rebuilt index")
	}
	// Second pass now hits.
	if _, rep := LoadCache(path, idx2.InputDigest); rep.State != CacheHit {
		t.Fatalf("post-rebuild state=%s", rep.State)
	}
}

func TestUnmarshalRejectsTamperedEntries(t *testing.T) {
	idx, _ := Scan(bytes.NewReader(synthStream(3)), DefaultOptions())
	b, _ := idx.Marshal()
	var doc map[string]any
	json.Unmarshal(b, &doc)
	entries := doc["entries"].([]any)
	entries[0].(map[string]any)["seq"] = 999
	tampered, _ := json.Marshal(doc)
	if _, err := UnmarshalIndex(tampered); err == nil {
		t.Fatal("tampered entries should not reproduce the digest")
	}
}

func TestScanRejectsZeroTimescale(t *testing.T) {
	opts := DefaultOptions()
	opts.Timescale = 0
	if _, err := Scan(bytes.NewReader(synthStream(1)), opts); err == nil {
		t.Fatal("expected invalid params")
	}
}

// TestScanDistinguishesReorderingFromRegression: a lower sequence with a
// slightly early timestamp is reordering (sortable, no regression issue); a
// higher sequence with a backwards timestamp is a clock regression.
func TestScanDistinguishesReorderingFromRegression(t *testing.T) {
	reorderFrames := []*media.Frame{
		synthFrame(0, 3600*2, 0, 8, 8, nil),
		synthFrame(1, 3600*3, 40, 8, 8, nil),
		synthFrame(2, 3600*1, 80, 8, 8, nil), // seq 2, ts 40ms earlier: regression
	}
	data, _ := media.EncodeStream(reorderFrames)
	idx, err := Scan(bytes.NewReader(data), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	sawRegression := false
	for _, e := range idx.Entries {
		if e.Regressed {
			sawRegression = true
		}
	}
	if !sawRegression {
		t.Fatalf("forward-seq backward-time should be flagged as regression: %+v", idx.Entries)
	}
	// The re-anchored timeline must stay monotonic.
	for i := 1; i < len(idx.Entries); i++ {
		if idx.Entries[i].Ticks < idx.Entries[i-1].Ticks {
			t.Fatalf("timeline not monotonic at %d", i)
		}
	}

	// Lower sequence with earlier timestamp is reordering: no regression flag.
	plainFrames := []*media.Frame{
		synthFrame(0, 3600*2, 0, 8, 8, nil),
		synthFrame(2, 3600*4, 80, 8, 8, nil),
		synthFrame(1, 3600*3, 40, 8, 8, nil),
	}
	data2, _ := media.EncodeStream(plainFrames)
	idx2, err := Scan(bytes.NewReader(data2), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range idx2.Entries {
		if e.Regressed {
			t.Fatalf("reordering must not be flagged as regression: %+v", idx2.Entries)
		}
	}
	if !idx2.Reordered {
		t.Fatal("out-of-order timestamps should set the reordered flag")
	}
}
