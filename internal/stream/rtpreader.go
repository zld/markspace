package stream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"mediaarchive/internal/diag"
)

// RTPFileResult is the bounded-memory summary of a streamed RTP log plus a
// sparse, deterministic set of manifest samples.
type RTPFileResult struct {
	Analysis *RTPAnalysis
	Samples  []Packet
	Total    int64
	Rate     int
}

// AnalyzeRTPReader streams a packet log without materialising it. The log
// format is one packet per line: "seq timestamp [ssrc] [payload]"; blank lines
// and '#' comments are ignored. Memory stays bounded by RTPOptions regardless
// of packet count, which is what allows million-packet archives to be
// processed under four workers.
func AnalyzeRTPReader(ctx context.Context, r io.Reader, opts RTPOptions) (*RTPFileResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	a := NewRTPAnalysis(opts)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	line := 0
	var total int64
	var spacing int64 = 1
	var samples []Packet
	maxSamples := 16
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, diag.New(diag.CodeCanceled, total, "rtp scan canceled after %d packets", total)
		}
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) < 2 {
			return nil, diag.New(diag.CodeRTPSegmentGap, int64(line), "line %d: want 'seq timestamp'", line)
		}
		seq, err := strconv.ParseUint(fields[0], 10, 16)
		if err != nil {
			return nil, diag.New(diag.CodeRTPSegmentGap, int64(line), "line %d: bad sequence %q", line, fields[0])
		}
		ts, err := strconv.ParseUint(fields[1], 10, 32)
		if err != nil {
			return nil, diag.New(diag.CodeRTPSegmentGap, int64(line), "line %d: bad timestamp %q", line, fields[1])
		}
		p := Packet{Seq: uint16(seq), Timestamp: uint32(ts), Offset: total}
		if len(fields) >= 3 {
			if v, err := strconv.ParseUint(fields[2], 10, 32); err == nil {
				p.SSRC = uint32(v)
			}
		}
		if len(fields) >= 4 {
			if v, err := strconv.ParseUint(fields[3], 10, 8); err == nil {
				p.Payload = uint8(v)
			}
		}
		a.Push(p)
		total++
		// Keep at most maxSamples evenly spaced samples, and the last packet.
		if total%spacing == 0 && len(samples) < maxSamples-1 {
			samples = append(samples, p)
		}
		for total/spacing >= int64(maxSamples-1)*2 {
			spacing *= 2
		}
	}
	if err := sc.Err(); err != nil {
		return nil, diag.New(diag.CodeTruncated, total, "rtp log read error after %d packets: %v", total, err)
	}
	if total == 0 {
		return nil, diag.New(diag.CodeMissingBase, 0, "rtp log has no packets")
	}
	// Replace the final sample slot with the true last packet for a closed
	// timeline without unbounded growth.
	samples = samples[:min(len(samples), maxSamples-1)]
	samples = append(samples, Packet{Seq: a.LastSeq, Timestamp: a.lastRawTS(), Offset: total - 1, SSRC: lastSSRC(a)})
	res := &RTPFileResult{Analysis: a.Finish(), Samples: samples, Total: total, Rate: a.ClockRate}
	return res, nil
}

// AnalyzeRTPFile opens a log read-only and streams it.
func AnalyzeRTPFile(ctx context.Context, path string, opts RTPOptions) (*RTPFileResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return AnalyzeRTPReader(ctx, f, opts)
}

func lastSSRC(a *RTPAnalysis) uint32 {
	var best uint32
	var n int64 = -1
	for ssrc, count := range a.SSRCs {
		if count > n {
			best, n = ssrc, count
		}
	}
	return best
}

// lastRawTS returns the last raw 32-bit timestamp observed on the leading
// edge, reconstructed from the unwrapped value.
func (a *RTPAnalysis) lastRawTS() uint32 { return uint32(a.lastTS) }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// String renders a one-line stream summary for CLI diagnostics.
func (r *RTPFileResult) String() string {
	return fmt.Sprintf("packets=%d missing=%d dup=%d ooo=%d jumps=%d duration=%.3fms",
		r.Total, r.Analysis.Missing, r.Analysis.Duplicates, r.Analysis.OutOfOrder,
		r.Analysis.TimestampJumps, r.Analysis.DurationMs())
}
