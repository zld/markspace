package slicer

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"hlscheck/internal/fsutil"
	"hlscheck/internal/model"
	"hlscheck/internal/ts"
)

// SegmentWriter executes the second pass: it re-reads the transport stream
// and copies raw 188-byte packets to per-segment temp files, publishing each
// segment with an atomic rename once its boundary range is complete.
type SegmentWriter struct {
	src    *os.File
	outDir string
	plan   *Plan
	prefix string
}

// NewSegmentWriter opens the second pass over srcPath. The same file handle
// is repositioned via pread-style Seek calls; only one 188-byte packet is
// buffered at a time.
func NewSegmentWriter(srcPath, outDir string, plan *Plan, prefix string) (*SegmentWriter, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	return &SegmentWriter{src: f, outDir: outDir, plan: plan, prefix: prefix}, nil
}

// Close releases the source file handle.
func (w *SegmentWriter) Close() error { return w.src.Close() }

// Write runs the packet copy and returns the canonical segment records. The
// final segment's end is the stream length; earlier segments end exactly at
// the next boundary's first packet offset.
func (w *SegmentWriter) Write() ([]model.Segment, error) {
	if _, err := w.src.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	cuts := w.plan.Cuts
	segments := make([]model.Segment, 0, len(cuts))
	pr := ts.NewPacketReader(bufio.NewReaderSize(w.src, 64*model.PacketSize))

	// Open segment 0 lazily once its first packet arrives.
	var (
		cur        *os.File
		curTmpName string
		curBytes   int64
		curIdx     = -1
	)
	closeCurrent := func(endPTS int64) error {
		if cur == nil {
			return nil
		}
		name := fmt.Sprintf("%s%d.ts", w.prefix, curIdx)
		finalPath, err := fsutil.SafeJoin(w.outDir, name)
		if err != nil {
			cur.Close()
			os.Remove(curTmpName)
			return err
		}
		if err := cur.Sync(); err != nil {
			cur.Close()
			os.Remove(curTmpName)
			return err
		}
		if err := cur.Close(); err != nil {
			os.Remove(curTmpName)
			return err
		}
		if err := os.Rename(curTmpName, finalPath); err != nil {
			return err
		}
		startPTS := cuts[curIdx].StartPTS
		durMS := model.FromTicks(endPTS - startPTS).Milliseconds()
		segments = append(segments, model.Segment{
			Seq: curIdx, File: name, StartPTS: startPTS, EndPTS: endPTS,
			DurationMSField: durMS, Bytes: curBytes,
		})
		cur = nil
		return nil
	}

	for {
		pkt, err := pr.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			if cur != nil {
				cur.Close()
				os.Remove(curTmpName)
			}
			return nil, err
		}
		// A boundary packet opens the next segment (segments start keyframe
		// aligned, so any cut offset matches a real packet offset).
		if curIdx+1 < len(cuts) && pkt.Offset == cuts[curIdx+1].StartOffset {
			endPTS := cuts[curIdx+1].StartPTS
			if err := closeCurrent(endPTS); err != nil {
				return nil, err
			}
			curIdx++
			name := fmt.Sprintf(".tmp-%s%d-", w.prefix, curIdx)
			f, err := os.CreateTemp(w.outDir, name)
			if err != nil {
				return nil, err
			}
			cur, curTmpName, curBytes = f, f.Name(), 0
		}
		// Drop packets before the first boundary (pre-roll before IDR 0).
		if curIdx < 0 {
			continue
		}
		if _, err := cur.Write(pkt.Raw()); err != nil {
			cur.Close()
			os.Remove(curTmpName)
			return nil, err
		}
		curBytes += int64(model.PacketSize)
	}
	if err := closeCurrent(w.plan.EndPTS); err != nil {
		return nil, err
	}
	return segments, nil
}
