package pipeline

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"mediatool/internal/fixture"
	"mediatool/internal/mapping"
	"mediatool/internal/mp4"
)

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestPipelineMemoryDoesNotGrowWithFragmentCount compares heap growth for
// two runs that differ only in fragment count. Because payloads are
// spooled to disk and metadata is O(events) metadata records (not bytes),
// the retained heap must not scale with the media volume.
func TestPipelineMemoryDoesNotGrowWithFragmentCount(t *testing.T) {
	measure := func(n int) uint64 {
		dir := filepath.Join(t.TempDir(), "out")
		g := fixture.Generate(7, n, -1)
		rules := []mapping.Rule{
			{OutputSlot: "video-main", TrackID: 1, MediaType: "video"},
			{OutputSlot: "audio-main", TrackID: 2, MediaType: "audio"},
		}
		pipe, err := New(rules, dir)
		if err != nil {
			t.Fatal(err)
		}
		pipe.SetTracks(g.Tracks)
		for i := 0; i < n; i++ {
			pipe.Ingest(Input{Data: g.VideoFrags[i].Bytes})
			pipe.Ingest(Input{Data: g.AudioFrags[i].Bytes})
		}
		if _, err := pipe.Finalize(); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapAlloc
	}

	small := measure(50)
	runtime.GC()
	large := measure(400)
	// The 8x larger media volume must not produce an 8x heap. We assert a
	// loose bound (under 3x) to keep the test deterministic across GC
	// timing while still catching a linear payload-buffer regression.
	if large > small*3 {
		t.Fatalf("heap grew with media volume: small=%d large=%d (ratio %.2f)",
			small, large, float64(large)/float64(small))
	}
}

// TestPipeline1000FragmentsEndToEnd is the headline performance contract:
// 1,000 fragments per track flow through every stage successfully.
func TestPipeline1000FragmentsEndToEnd(t *testing.T) {
	const n = 1000
	g := fixture.Generate(99, n, -1)
	dir := filepath.Join(t.TempDir(), "out")
	rules := []mapping.Rule{
		{OutputSlot: "video-main", TrackID: 1, MediaType: "video"},
		{OutputSlot: "audio-main", TrackID: 2, MediaType: "audio"},
	}
	pipe, err := New(rules, dir)
	if err != nil {
		t.Fatal(err)
	}
	pipe.SetTracks(g.Tracks)
	for i := 0; i < n; i++ {
		pipe.Ingest(Input{Data: g.VideoFrags[i].Bytes})
		pipe.Ingest(Input{Data: g.AudioFrags[i].Bytes})
	}
	res, err := pipe.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range res.Manifest.Slots {
		if len(slot.Fragments) != n {
			t.Fatalf("slot %s got %d chunks, want %d",
				slot.Slot, len(slot.Fragments), n)
		}
	}
	// Every remuxed chunk must re-parse cleanly through the streaming API.
	for _, slot := range res.Manifest.Slots {
		for _, f := range slot.Fragments {
			raw := mustReadFile(t, filepath.Join(dir, f.File))
			count := 0
			if serr := mp4.StreamFromBytes(raw, func(fr *mp4.Fragment, fatal error) error {
				if fatal != nil {
					return fatal
				}
				count++
				return nil
			}); serr != nil || count != 1 {
				t.Fatalf("output chunk %s invalid: err=%v count=%d", f.File, serr, count)
			}
		}
	}
}
