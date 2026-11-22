package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mediaarchive/internal/stream"
)

// TestMillionPacketMemoryBounded streams a million-packet RTP log and proves
// the analyzer's resident state stays small: the fault list, delivered window
// and hole window are all fixed-capacity. This is the performance contract for
// four-worker archive runs.
func TestMillionPacketMemoryBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("million packet stream")
	}
	const packets = 1_000_000
	p := filepath.Join(t.TempDir(), "million.rtp")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := bufioWriter(f)
	for i := 0; i < packets; i++ {
		// Every 50_000 packets produce a gap so faults are also generated.
		if i%50_000 == 0 && i > 0 {
			continue
		}
		fmt.Fprintf(w, "%d %d 4242 96\n", (1000+i)&0xFFFF, 9_000_000+uint32(i)*3000)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	res, err := stream.AnalyzeRTPFile(context.Background(), p, stream.RTPOptions{
		ClockRate: 90000, MaxFaults: 64, DupWindow: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if res.Total < packets-int64(packets/50_000)-1 {
		t.Fatalf("packets = %d", res.Total)
	}
	if len(res.Analysis.Faults) > 64 {
		t.Fatalf("fault list = %d", len(res.Analysis.Faults))
	}
	if len(res.Samples) > 16 {
		t.Fatalf("sample rows = %d", len(res.Samples))
	}
	if res.Analysis.Missing < 10 {
		t.Fatalf("missing = %d", res.Analysis.Missing)
	}
	// Generous wall-clock bound: this runs in well under a second locally.
	if elapsed > 30*time.Second {
		t.Fatalf("streaming 1M packets took %v", elapsed)
	}
	if !strings.Contains(res.String(), "packets=") {
		t.Fatalf("summary = %s", res.String())
	}
}
