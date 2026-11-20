package validate

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"hlscheck/internal/fsutil"
	"hlscheck/internal/hls"
)

// DropRequest names one segment file to remove: rendition bitrate + seq.
type DropRequest struct {
	Bps int
	Seq int
}

// SwapRequest names two playlist positions to exchange within one rendition.
type SwapRequest struct {
	Bps int
	A   int
	B   int
}

// ApplyDrops deletes the requested segment files. Missing files are reported
// as errors so callers cannot silently inject a no-op fault.
func ApplyDrops(root string, idx *hls.Index, drops []DropRequest) ([]string, error) {
	var applied []string
	for _, req := range drops {
		ir := findRendition(idx, req.Bps)
		if ir == nil {
			return applied, fmt.Errorf("drop: unknown rendition %d bps", req.Bps)
		}
		if req.Seq < 0 || req.Seq >= len(ir.Segments) {
			return applied, fmt.Errorf("drop: seq %d out of range for %d bps (have %d)",
				req.Seq, req.Bps, len(ir.Segments))
		}
		path, err := fsutil.SafeJoin(root, ir.Rendition.Dir, ir.Segments[req.Seq].File)
		if err != nil {
			return applied, err
		}
		if err := os.Remove(path); err != nil {
			return applied, fmt.Errorf("drop %d:%d: %w", req.Bps, req.Seq, err)
		}
		applied = append(applied, fmt.Sprintf("%d:%d", req.Bps, req.Seq))
	}
	return applied, nil
}

// ApplySwaps exchanges the non-tag URI lines at two playlist positions. The
// EXTINF lines stay in place, which is exactly the out-of-order condition the
// validator's ORDER_MISMATCH check is designed to catch.
func ApplySwaps(root string, idx *hls.Index, swaps []SwapRequest) ([]string, error) {
	var applied []string
	for _, req := range swaps {
		ir := findRendition(idx, req.Bps)
		if ir == nil {
			return applied, fmt.Errorf("swap: unknown rendition %d bps", req.Bps)
		}
		path, err := fsutil.SafeJoinRel(root, ir.Playlist)
		if err != nil {
			return applied, err
		}
		lines, err := readLines(path)
		if err != nil {
			return applied, err
		}
		uriPos := uriPositions(lines)
		if req.A < 0 || req.A >= len(uriPos) || req.B < 0 || req.B >= len(uriPos) {
			return applied, fmt.Errorf("swap %d:%d:%d: position out of range (have %d entries)",
				req.Bps, req.A, req.B, len(uriPos))
		}
		ia, ib := uriPos[req.A], uriPos[req.B]
		lines[ia], lines[ib] = lines[ib], lines[ia]
		if err := writeLinesAtomic(path, lines); err != nil {
			return applied, err
		}
		applied = append(applied, fmt.Sprintf("%d:%d:%d", req.Bps, req.A, req.B))
	}
	return applied, nil
}

// findRendition locates a variant by bitrate.
func findRendition(idx *hls.Index, bps int) *hls.IndexRendition {
	for i := range idx.Renditions {
		if idx.Renditions[i].Rendition.Bps == bps {
			return &idx.Renditions[i]
		}
	}
	return nil
}

// uriPositions returns the line indices of playlist URI entries (non-tags).
func uriPositions(lines []string) []int {
	var pos []int
	for i, line := range lines {
		if line != "" && !strings.HasPrefix(line, "#") {
			pos = append(pos, i)
		}
	}
	return pos
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// writeLinesAtomic rewrites a playlist through a temp file and rename so an
// interrupted fault injection cannot leave a half-written manifest.
func writeLinesAtomic(path string, lines []string) error {
	data := strings.Join(lines, "\n") + "\n"
	return fsutil.AtomicWriteFile(path, []byte(data), 0o644)
}
