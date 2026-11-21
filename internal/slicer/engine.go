package slicer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"streamcheck/internal/ts"
)

// EngineConfig configures one variant's segmentation engine.
type EngineConfig struct {
	Window        WindowConfig
	VideoPID      uint16 // 0 means auto-detect from the PMT
	SegmentPrefix string
}

// CutEvent reports one closed segment to the caller.
type CutEvent struct {
	Record SegmentRecord
	Bytes  int64
}

// Engine consumes parsed TS packets for one variant, cuts segments on video
// keyframes and writes each segment as a standalone .ts file.
type Engine struct {
	cfg        EngineConfig
	window     *Window
	pes        *ts.PESReassembler
	assemblers map[uint16]*ts.SectionAssembler
	pmtPIDs    map[uint16]bool
	pmt        *ts.PMT
	videoPID   uint16
	out        *os.File
	outPath    string
	tmpPath    string
	outBytes   int64
	open       bool
}

// NewEngine builds an engine; the output directory is validated lazily.
func NewEngine(cfg EngineConfig) *Engine {
	return &Engine{
		cfg:        cfg,
		window:     NewWindow(cfg.Window),
		pes:        ts.NewPESReassembler(0),
		assemblers: make(map[uint16]*ts.SectionAssembler),
		pmtPIDs:    make(map[uint16]bool),
		videoPID:   cfg.VideoPID,
	}
}

// Feed pushes one parsed packet. Table PIDs update PAT/PMT state; the video
// PID drives keyframe-aware cutting; every packet (including PSI tables and
// null filler packets) is copied verbatim into the open segment so the
// output .ts stays decodable and preserves the source's effective bitrate.
func (e *Engine) Feed(p *ts.Packet, dir string) (*CutEvent, error) {
	// Tables update parsing state first, but are still written below.
	switch {
	case p.PID == ts.PIDPAT:
		if err := e.feedTable(p, true); err != nil {
			return nil, err
		}
	case e.pmtPIDs[p.PID]:
		if err := e.feedTable(p, false); err != nil {
			return nil, err
		}
	}
	if err := e.ensureSegment(dir); err != nil {
		return nil, err
	}
	if err := e.writePacket(p); err != nil {
		return nil, err
	}
	// Only the video elementary stream drives cut decisions.
	if e.videoPID == 0 || p.PID != e.videoPID {
		return nil, nil
	}
	return e.feedVideo(p)
}

// feedTable reassembles PAT/PMT sections and learns the video PID.
func (e *Engine) feedTable(p *ts.Packet, isPAT bool) error {
	a := e.assemblers[p.PID]
	if a == nil {
		a = &ts.SectionAssembler{}
		e.assemblers[p.PID] = a
	}
	sec := a.Push(*p)
	if sec == nil {
		return nil
	}
	if isPAT {
		progs, err := ts.ParsePAT(*sec)
		if err != nil {
			return fmt.Errorf("PAT: %w", err)
		}
		for _, pr := range progs {
			e.pmtPIDs[pr.PMTPID] = true
		}
		return nil
	}
	pmt, err := ts.ParsePMT(*sec)
	if err != nil {
		return fmt.Errorf("PMT: %w", err)
	}
	e.pmt = &pmt
	if e.videoPID == 0 {
		e.videoPID = pmt.VideoPID()
	}
	return nil
}

// feedVideo handles video packets. A PUSI packet starts a fresh, complete
// PES unit (the fixture and common muxers emit one frame per PUSI), so it
// is processed immediately; continuation fragments are buffered through the
// reassembler for streams that split a PES across multiple packets.
func (e *Engine) feedVideo(p *ts.Packet) (*CutEvent, error) {
	if p.PUSI {
		return e.handlePES(p.Payload())
	}
	// Continuation fragment: accumulate and process only when a later PUSI
	// completes the unit. This path is rare for frame-sized PES packets.
	completed, _ := e.pes.Push(*p)
	if completed == nil {
		return nil, nil
	}
	return e.handlePES(completed)
}

// Flush closes any in-flight PES unit and the open segment at end of stream.
func (e *Engine) Flush(dir string) (*CutEvent, error) {
	if e.videoPID != 0 {
		if tail := e.pes.Drain(); len(tail) > 0 {
			if ev, err := e.handlePES(tail); err != nil {
				return nil, err
			} else if ev != nil {
				return ev, nil
			}
		}
	}
	return e.closeSegment(dir, e.window.lastPTSOrZero(), false)
}

func (e *Engine) handlePES(unit []byte) (*CutEvent, error) {
	hdr, err := ts.ParsePES(unit)
	if err != nil && !hdr.Truncated {
		return nil, err
	}
	if !hdr.HasPTS {
		return nil, nil
	}
	exp, _ := e.window.ObservePTS(hdr.PTS)
	keyframe := ts.IsH264Keyframe(hdr.Data)
	if !e.window.openHasPTS && keyframe {
		e.window.OpenSegment(exp)
	}
	if e.window.ShouldCut(keyframe, exp) {
		ev, err := e.closeSegment(filepath.Dir(e.outPath), exp, keyframe)
		if err != nil {
			return nil, err
		}
		// The boundary keyframe starts the next segment immediately so the
		// following frames accumulate against a fresh PTS origin.
		if keyframe {
			e.window.OpenSegment(exp)
		}
		return ev, nil
	}
	return nil, nil
}

func (e *Engine) ensureSegment(dir string) error {
	if e.open {
		return nil
	}
	seq := e.window.NextSequence()
	name := fmt.Sprintf("%s%05d.ts", e.cfg.SegmentPrefix, seq)
	final := filepath.Join(dir, name)
	// Write to a sibling temp file and rename so a crash never leaves a
	// partial segment at its final, playlist-referenced name.
	tmp, err := os.CreateTemp(dir, ".seg-*.tmp")
	if err != nil {
		return err
	}
	e.out = tmp
	e.tmpPath = tmp.Name()
	e.outPath = final
	e.outBytes = 0
	e.open = true
	return nil
}

func (e *Engine) writePacket(p *ts.Packet) error {
	if !e.open {
		return nil
	}
	n, err := e.out.Write(p.Data)
	e.outBytes += int64(n)
	return err
}

func (e *Engine) closeSegment(dir string, endPTS uint64, keyframe bool) (*CutEvent, error) {
	if !e.open {
		return nil, nil
	}
	if err := e.out.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(e.tmpPath, e.outPath); err != nil {
		_ = os.Remove(e.tmpPath)
		return nil, err
	}
	rec := e.window.CloseSegment(filepath.Base(e.outPath), endPTS, keyframe)
	ev := &CutEvent{Record: rec, Bytes: e.outBytes}
	e.open = false
	e.out = nil
	return ev, nil
}

// Window exposes the segment window for manifest generation.
func (e *Engine) Window() *Window { return e.window }

// VideoPID returns the detected video PID.
func (e *Engine) VideoPID() uint16 { return e.videoPID }

// lastPTSOrZero returns the most recent expanded PTS.
func (w *Window) lastPTSOrZero() uint64 { return w.lastPTS }

// Run drives an engine from a packet source until EOF.
func Run(e *Engine, src PacketSource, dir string) ([]CutEvent, error) {
	var events []CutEvent
	for {
		p, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return events, err
		}
		if p == nil {
			break
		}
		ev, err := e.Feed(p, dir)
		if err != nil {
			return events, err
		}
		if ev != nil {
			events = append(events, *ev)
		}
	}
	ev, err := e.Flush(dir)
	if err != nil {
		return events, err
	}
	if ev != nil {
		events = append(events, *ev)
	}
	return events, nil
}

// PacketSource abstracts a stream of parsed TS packets.
type PacketSource interface {
	Next() (*ts.Packet, error)
}

// scannerSource adapts a ts.Scanner to the PacketSource interface.
type scannerSource struct{ s *ts.Scanner }

// NewScannerSource wraps a TS scanner as a PacketSource.
func NewScannerSource(s *ts.Scanner) PacketSource { return &scannerSource{s: s} }

func (c *scannerSource) Next() (*ts.Packet, error) {
	p := c.s.Next()
	if p == nil {
		if err := c.s.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	return p, nil
}
