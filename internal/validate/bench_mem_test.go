package validate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/fixtures"
)

// TestRTPMemoryBound checks that peak heap stays below 2x the input size for
// a multi-megabyte RTP recording. The compile-time target is 1 GB; this test
// uses a smaller file so it runs in CI while exercising the same code path.
func TestRTPMemoryBound(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "large.rtp")
	const packets = 20000
	const payloadSize = 1200
	ts := make([]uint32, packets)
	for i := range ts {
		ts[i] = uint32(i) * 3000
	}
	layout, err := fixtures.WriteRTPTimestampsSized(path, 90000, ts, payloadSize, 7)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res, err := FromFiles(Files{Name: "mem", Window: clock.FromMilliseconds(2000), RTP: path})
	if err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if res.PacketCount != packets {
		t.Fatalf("packets=%d", res.PacketCount)
	}
	// Heap growth is measured after the parse result is still reachable,
	// which is a conservative upper bound.
	used := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if used < 0 {
		used = 0
	}
	if used > 2*st.Size() {
		t.Fatalf("heap %d bytes exceeds 2x input %d bytes", used, st.Size())
	}
	t.Logf("input=%d bytes heap_delta=%d bytes packets=%d", st.Size(), used, len(ts))
	_ = layout
}
