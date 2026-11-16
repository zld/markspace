// Package cut slices an MPEG-TS stream into HLS segments on IDR
// keyframe boundaries: the cut is made at the first keyframe at or
// after the target duration; sparse keyframes simply extend segments
// and a short final tail is kept.
package cut

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"

	"hlscheck/internal/store"
	"hlscheck/internal/ts"
)

// Options controls slicing.
type Options struct {
	TargetDuration float64 // seconds
}

// SegmentInfo describes one published segment file.
type SegmentInfo struct {
	Seq      int
	Name     string
	Duration float64 // seconds
	StartPTS int64   // unwrapped 90 kHz
	EndPTS   int64   // unwrapped 90 kHz of the last frame
	SHA256   string  // lowercase hex
	Bytes    int64
}

// Result is the outcome of slicing one input.
type Result struct {
	Segments  []SegmentInfo
	Anomalies []ts.Anomaly
}

// Slice streams TS packets from r and publishes segments into outDir.
// Input without any keyframe is an error; packets before the first
// keyframe are dropped. On error or cancellation no segment is left
// published in a half-written state (temp files are aborted).
func Slice(ctx context.Context, r io.Reader, outDir string, opt Options) (*Result, error) {
	if opt.TargetDuration <= 0 {
		opt.TargetDuration = 4
	}
	if err := store.CleanTemps(outDir); err != nil {
		return nil, err
	}
	s := &slicer{
		rd:  ts.NewReader(r),
		pm:  ts.NewProgramMap(),
		dir: outDir,
		tgt: int64(opt.TargetDuration * ts.ClockRate),
	}
	err := s.run(ctx)
	if err != nil {
		s.abort()
		return nil, err
	}
	if len(s.res.Segments) == 0 {
		return nil, fmt.Errorf("no keyframes found: cannot slice")
	}
	s.res.Anomalies = s.rd.Anomalies()
	return &s.res, nil
}

type slicer struct {
	rd  *ts.Reader
	pm  *ts.ProgramMap
	dir string
	tgt int64

	cur      *store.AtomicFile
	h        hash.Hash
	n        int64 // bytes in current segment
	segStart int64
	unwrap   ts.PTSUnwrapper
	lastPTS  int64
	frameInt int64 // latest inter-frame PTS delta
	prevPTS  int64
	probed   int
	res      Result
}

func (s *slicer) run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, err := s.rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		s.pm.Feed(p)
		if s.pm.Video < 0 {
			// Fallback: adopt the first PID carrying PES with PTS.
			if s.probed++; s.probed > 500 && p.PUSI {
				if h, pe := ts.ParsePES(p.Payload); pe == nil && h.PTS >= 0 {
					s.pm.Video = int(p.PID)
				}
			}
			continue
		}
		if p.PUSI && p.PID == uint16(s.pm.Video) {
			if err := s.onPES(p); err != nil {
				return err
			}
		}
		if s.cur == nil {
			continue // drop packets before the first keyframe
		}
		if _, err := s.cur.Write(p.Raw); err != nil {
			return err
		}
		s.h.Write(p.Raw)
		s.n += int64(len(p.Raw))
	}
	return s.closeSeg()
}

// onPES handles the first packet of a PES: track PTS and cut at IDR
// keyframes once the target duration has elapsed.
func (s *slicer) onPES(p *ts.Packet) error {
	h, err := ts.ParsePES(p.Payload)
	if err != nil {
		return nil // not a PES start we understand; ignore
	}
	pts := int64(-1)
	if h.PTS >= 0 {
		pts = s.unwrap.Unwrap(h.PTS)
		if s.prevPTS > 0 && pts > s.prevPTS {
			s.frameInt = pts - s.prevPTS
		}
		s.prevPTS, s.lastPTS = pts, pts
	}
	if !ts.HasIDR(p.Payload[h.PayloadOffset:]) {
		return nil
	}
	if pts < 0 {
		pts = s.lastPTS
	}
	if s.cur == nil {
		return s.openSeg(pts)
	}
	if pts-s.segStart >= s.tgt {
		if err := s.closeAt(pts); err != nil {
			return err
		}
		return s.openSeg(pts)
	}
	return nil
}

func (s *slicer) openSeg(pts int64) error {
	name := fmt.Sprintf("seg_%05d.ts", len(s.res.Segments))
	f, err := store.Create(s.dir, name)
	if err != nil {
		return err
	}
	s.cur, s.h, s.n, s.segStart = f, sha256.New(), 0, pts
	s.res.Segments = append(s.res.Segments, SegmentInfo{
		Seq: len(s.res.Segments), Name: name, StartPTS: pts,
	})
	return nil
}

// closeSeg finalizes the tail segment at EOF; its duration extends one
// frame interval past the last PTS.
func (s *slicer) closeSeg() error {
	return s.finish(s.lastPTS+s.frameInt, s.lastPTS)
}

// closeAt finalizes the current segment at a keyframe boundary.
func (s *slicer) closeAt(boundary int64) error {
	return s.finish(boundary, boundary-s.frameInt)
}

func (s *slicer) finish(end, lastFramePTS int64) error {
	if s.cur == nil {
		return nil
	}
	if err := s.cur.Close(); err != nil {
		return err
	}
	seg := &s.res.Segments[len(s.res.Segments)-1]
	seg.EndPTS = lastFramePTS
	dur := float64(end-s.segStart) / ts.ClockRate
	if dur <= 0 {
		dur = 0.04
	}
	seg.Duration = dur
	seg.SHA256 = hex.EncodeToString(s.h.Sum(nil))
	seg.Bytes = s.n
	s.cur = nil
	return nil
}

func (s *slicer) abort() {
	if s.cur != nil {
		s.cur.Abort()
		s.cur = nil
	}
}
