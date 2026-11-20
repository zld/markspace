package validate

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"

	"hlscheck/internal/fsutil"
	"hlscheck/internal/hls"
	"hlscheck/internal/model"
	"hlscheck/internal/ts"
)

// playlistMatches regenerates the expected playlist from the index and
// compares it byte-for-byte with the file on disk.
func playlistMatches(root string, ir *hls.IndexRendition, targetSeconds int) (bool, error) {
	path, err := fsutil.SafeJoinRel(root, ir.Playlist)
	if err != nil {
		return false, err
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	expected := hls.MediaPlaylist(*ir, targetSeconds)
	return bytes.Equal(onDisk, expected), nil
}

// frameTicks converts the index frame rate to 90 kHz PTS ticks.
func frameTicks(frameRate int) int64 {
	if frameRate <= 0 {
		return model.FrameTolerance
	}
	return model.ClockRate / int64(frameRate)
}

// checkRendition runs all single-rendition checks and returns every finding.
func checkRendition(root string, ir *hls.IndexRendition, idx *hls.Index) []model.Finding {
	var findings []model.Finding
	bps := ir.Rendition.Bps
	seen := make(map[string]int, len(ir.Segments))
	missingReported := make(map[int]bool)
	for _, seg := range ir.Segments {
		path, err := fsutil.SafeJoin(root, ir.Rendition.Dir, seg.File)
		if err != nil {
			findings = append(findings, model.Finding{
				Code: CodeDanglingReference, Bps: bps, Seq: seg.Seq, Detail: err.Error(),
			})
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			findings = append(findings, model.Finding{
				Code: CodeMissingSegment, Bps: bps, Seq: seg.Seq,
				Detail: fmt.Sprintf("segment %s not found on disk", seg.File),
			})
			missingReported[seg.Seq] = true
			continue
		}
		if info.Size() != seg.Bytes {
			findings = append(findings, model.Finding{
				Code: "SEGMENT_SIZE_MISMATCH", Bps: bps, Seq: seg.Seq,
				Detail: fmt.Sprintf("size %d != indexed %d", info.Size(), seg.Bytes),
			})
		}
		if prev, dup := seen[seg.File]; dup {
			findings = append(findings, model.Finding{
				Code: CodeDuplicateSegment, Bps: bps, Seq: seg.Seq,
				Detail: fmt.Sprintf("file %s also used by seq %d", seg.File, prev),
			})
		}
		seen[seg.File] = seg.Seq

		// Duration deviation beyond one frame against the nominal target.
		nominalMS := int64(idx.TargetSeconds) * 1000
		deltaMS := seg.DurationMS() - nominalMS
		if deltaMS < 0 {
			deltaMS = -deltaMS
		}
		frameMS := (frameTicks(idx.FrameRate) * 1000) / model.ClockRate
		// The final short segment is allowed; only interior ones are checked.
		isFinal := seg.Seq == len(ir.Segments)-1
		if !isFinal && deltaMS > frameMS {
			findings = append(findings, model.Finding{
				Code: CodeDurationDeviation, Bps: bps, Seq: seg.Seq,
				Detail: fmt.Sprintf("duration %d ms deviates from target %d s by %d ms",
					seg.DurationMS(), idx.TargetSeconds, deltaMS),
			})
		}
	}

	// 2. Playlist order and references, plus canonical regeneration match.
	findings = append(findings, checkPlaylistOrder(root, ir, bps, missingReported)...)
	if ok, err := playlistMatches(root, ir, idx.TargetSeconds); err != nil {
		findings = append(findings, model.Finding{
			Code: CodePlaylistDrift, Bps: bps, Detail: "cannot read playlist: " + err.Error(),
		})
	} else if !ok {
		findings = append(findings, model.Finding{
			Code: CodePlaylistDrift, Bps: bps,
			Detail: "playlist content differs from index-derived canonical form",
		})
	}

	// 3. TS continuity counters inside every present segment.
	findings = append(findings, checkContinuity(root, ir, bps)...)
	return findings
}

// checkPlaylistOrder parses the media playlist and verifies sequence order,
// uniqueness and that every referenced file exists in the index and on disk.
func checkPlaylistOrder(root string, ir *hls.IndexRendition, bps int, missingReported map[int]bool) []model.Finding {
	var findings []model.Finding
	path, err := fsutil.SafeJoinRel(root, ir.Playlist)
	if err != nil {
		return append(findings, model.Finding{Code: CodeDanglingReference, Bps: bps, Detail: err.Error()})
	}
	f, err := os.Open(path)
	if err != nil {
		return append(findings, model.Finding{Code: CodePlaylistDrift, Bps: bps, Detail: err.Error()})
	}
	defer f.Close()

	indexed := make(map[string]int, len(ir.Segments))
	for _, s := range ir.Segments {
		indexed[s.File] = s.Seq
	}
	var order []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		order = append(order, line)
	}
	for seq, file := range order {
		if want, ok := indexed[file]; !ok {
			findings = append(findings, model.Finding{
				Code: CodeDanglingReference, Bps: bps, Seq: seq,
				Detail: "playlist references file absent from index: " + file,
			})
			continue
		} else if want != seq {
			findings = append(findings, model.Finding{
				Code: CodeOrderMismatch, Bps: bps, Seq: seq,
				Detail: fmt.Sprintf("playlist position %d holds %s (index seq %d)", seq, file, want),
			})
		}
		// Existence on disk (the file may have been dropped by fault injection).
		segPath, err := fsutil.SafeJoin(root, ir.Rendition.Dir, file)
		if err == nil {
			if _, statErr := os.Stat(segPath); statErr != nil && !missingReported[seq] {
				findings = append(findings, model.Finding{
					Code: CodeMissingSegment, Bps: bps, Seq: seq,
					Detail: "playlist references missing file: " + file,
				})
			}
		}
	}
	return findings
}

// checkContinuity re-parses each present segment and reports duplicate or
// jumping continuity counters, keyed by rendition and segment sequence.
func checkContinuity(root string, ir *hls.IndexRendition, bps int) []model.Finding {
	var findings []model.Finding
	for _, seg := range ir.Segments {
		path, err := fsutil.SafeJoin(root, ir.Rendition.Dir, seg.File)
		if err != nil {
			continue // reported as a path/dangling finding elsewhere
		}
		f, err := os.Open(path)
		if err != nil {
			continue // missing segments are reported elsewhere
		}
		tracker := ts.NewContinuityTracker()
		pr := ts.NewPacketReader(bufio.NewReaderSize(f, 64*model.PacketSize))
		for {
			pkt, perr := pr.ReadPacket()
			if perr != nil {
				break
			}
			if ev, _ := tracker.Observe(pkt); ev == ts.CCDuplicate || ev == ts.CCJump {
				findings = append(findings, model.Finding{
					Code: CodeCCError, Bps: bps, Seq: seg.Seq,
					Detail: fmt.Sprintf("%s at offset %d (cc=%d)", ev, pkt.Offset, pkt.ContinuityCounter),
				})
			}
		}
		f.Close()
	}
	return findings
}

// checkCrossRendition verifies that all variants share segment count and
// that every boundary PTS lies within one frame of the reference rendition.
func checkCrossRendition(idx *hls.Index) []model.Finding {
	var findings []model.Finding
	if len(idx.Renditions) < 2 {
		return findings
	}
	tol := frameTicks(idx.FrameRate)
	ref := idx.Renditions[0]
	for i := 1; i < len(idx.Renditions); i++ {
		other := idx.Renditions[i]
		if len(other.Segments) != len(ref.Segments) {
			findings = append(findings, model.Finding{
				Code: CodeCountMismatch, Bps: other.Rendition.Bps,
				Detail: fmt.Sprintf("%d segments vs %d on %d bps",
					len(other.Segments), len(ref.Segments), ref.Rendition.Bps),
			})
		}
		n := len(ref.Segments)
		if len(other.Segments) < n {
			n = len(other.Segments)
		}
		for j := 0; j < n; j++ {
			delta := other.Segments[j].StartPTS - ref.Segments[j].StartPTS
			if delta < 0 {
				delta = -delta
			}
			if delta > tol {
				findings = append(findings, model.Finding{
					Code: CodeBoundaryMisalign, Bps: other.Rendition.Bps, Seq: j,
					Detail: fmt.Sprintf("boundary %d off by %d ticks (limit %d)", j, delta, tol),
				})
			}
		}
	}
	return findings
}
