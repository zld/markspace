package stream

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/pion/rtp"

	"mediaarchive/internal/diag"
)

// RTPCaptureMagic marks the binary capture format written by the fixture
// generator: four magic bytes followed by length-prefixed raw RTP packets.
// Text logs remain supported so operators can ship human-readable notes.
var RTPCaptureMagic = [4]byte{'A', 'R', 'P', '1'}

// RTPFileResult is the bounded-memory summary of a streamed RTP capture plus a
// sparse, deterministic set of manifest samples.
type RTPFileResult struct {
	Analysis *RTPAnalysis
	Samples  []Packet
	Total    int64
	Rate     int
}

// AnalyzeRTPReader streams a capture without materialising it. Binary captures
// are decoded with pion/rtp so header interpretation exactly matches a
// receiver; text logs use the documented "seq timestamp [ssrc] [payload]"
// form. Memory stays bounded by RTPOptions regardless of packet count.
func AnalyzeRTPReader(ctx context.Context, r io.Reader, opts RTPOptions) (*RTPFileResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	br := bufio.NewReaderSize(r, 1<<20)
	magic, err := br.Peek(len(RTPCaptureMagic))
	if err != nil && err != io.EOF && len(magic) < len(RTPCaptureMagic) {
		return nil, diag.New(diag.CodeTruncated, 0, "capture shorter than magic: %v", err)
	}
	if len(magic) == len(RTPCaptureMagic) && [4]byte(magic) == RTPCaptureMagic {
		if _, err := br.Discard(len(RTPCaptureMagic)); err != nil {
			return nil, err
		}
		return analyzeRTPBinary(ctx, br, opts)
	}
	return analyzeRTPText(ctx, br, opts)
}

// AnalyzeRTPFile opens a capture read-only and streams it.
func AnalyzeRTPFile(ctx context.Context, path string, opts RTPOptions) (*RTPFileResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return AnalyzeRTPReader(ctx, f, opts)
}

// packetSampler retains at most maxSamples evenly spaced packets with
// logarithmic spacing, so a billion-packet capture still yields a fixed-size
// manifest excerpt.
type packetSampler struct {
	max      int
	spacing  int64
	total    int64
	retained []Packet
}

func newPacketSampler(max int) *packetSampler {
	if max < 2 {
		max = 2
	}
	return &packetSampler{max: max, spacing: 1}
}

func (s *packetSampler) add(p Packet) {
	s.total++
	if s.total%s.spacing == 0 && len(s.retained) < s.max-1 {
		s.retained = append(s.retained, p)
	}
	for s.total/s.spacing >= int64(s.max-1)*2 {
		s.spacing *= 2
	}
}

func (s *packetSampler) finish(last Packet) []Packet {
	out := s.retained[:minInt(len(s.retained), s.max-1)]
	return append(out, last)
}

func analyzeRTPBinary(ctx context.Context, r io.Reader, opts RTPOptions) (*RTPFileResult, error) {
	a := NewRTPAnalysis(opts)
	sampler := newPacketSampler(16)
	var hdr [2]byte
	var buf []byte
	var last Packet
	for {
		if err := ctx.Err(); err != nil {
			return nil, diag.New(diag.CodeCanceled, sampler.total, "rtp scan canceled after %d packets", sampler.total)
		}
		_, err := io.ReadFull(r, hdr[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, diag.New(diag.CodeTruncated, sampler.total, "truncated packet length after %d packets", sampler.total)
		}
		n := int(binary.BigEndian.Uint16(hdr[:]))
		if n < 12 || n > 65535 {
			return nil, diag.New(diag.CodeRTPSegmentGap, sampler.total, "invalid RTP packet length %d", n)
		}
		if cap(buf) < n {
			buf = make([]byte, n)
		}
		buf = buf[:n]
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, diag.New(diag.CodeTruncated, sampler.total, "truncated RTP packet after %d packets", sampler.total)
		}
		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf); err != nil {
			return nil, diag.New(diag.CodeRTPSegmentGap, sampler.total, "invalid RTP header: %v", err)
		}
		p := FromRTP(&pkt, sampler.total)
		a.Push(p)
		sampler.add(p)
		last = p
	}
	if sampler.total == 0 {
		return nil, diag.New(diag.CodeMissingBase, 0, "rtp capture has no packets")
	}
	return &RTPFileResult{
		Analysis: a.Finish(), Samples: sampler.finish(last),
		Total: sampler.total, Rate: a.ClockRate,
	}, nil
}

func analyzeRTPText(ctx context.Context, r io.Reader, opts RTPOptions) (*RTPFileResult, error) {
	a := NewRTPAnalysis(opts)
	sampler := newPacketSampler(16)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	line := 0
	var last Packet
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, diag.New(diag.CodeCanceled, sampler.total, "rtp scan canceled after %d packets", sampler.total)
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
		p := Packet{Seq: uint16(seq), Timestamp: uint32(ts), Offset: sampler.total}
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
		sampler.add(p)
		last = p
	}
	if err := sc.Err(); err != nil {
		return nil, diag.New(diag.CodeTruncated, sampler.total, "rtp log read error after %d packets: %v", sampler.total, err)
	}
	if sampler.total == 0 {
		return nil, diag.New(diag.CodeMissingBase, 0, "rtp log has no packets")
	}
	return &RTPFileResult{
		Analysis: a.Finish(), Samples: sampler.finish(last),
		Total: sampler.total, Rate: a.ClockRate,
	}, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// WriteRTPCapture serialises packets into the binary capture format, using
// pion/rtp for marshalling so fixture bytes are real RTP.
func WriteRTPCapture(w io.Writer, packets []*rtp.Packet) error {
	if _, err := w.Write(RTPCaptureMagic[:]); err != nil {
		return err
	}
	var hdr [2]byte
	for _, p := range packets {
		raw, err := p.Marshal()
		if err != nil {
			return err
		}
		binary.BigEndian.PutUint16(hdr[:], uint16(len(raw)))
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
	}
	return nil
}

// String renders a one-line stream summary for CLI diagnostics.
func (r *RTPFileResult) String() string {
	return fmt.Sprintf("packets=%d missing=%d dup=%d ooo=%d jumps=%d duration=%.3fms",
		r.Total, r.Analysis.Missing, r.Analysis.Duplicates, r.Analysis.OutOfOrder,
		r.Analysis.TimestampJumps, r.Analysis.DurationMs())
}
