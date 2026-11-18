package rtp

import (
	"sync"
	"time"
)

// PipelineStats is a point-in-time snapshot of every pipeline counter.
type PipelineStats struct {
	ParseErrors       int
	Received          int
	Duplicates        int
	LatePackets       int
	MissingPackets    int
	GapEvents         int
	TimeoutFlushes    int
	CompleteFrames    int
	IncompleteFrames  int
}

// Pipeline wires the parser, reorderer and frame reassembler into the single
// entry point used by both RTSP transport adapters. It is safe for the
// gortsplib callback goroutine and an external timeout ticker concurrently.
type Pipeline struct {
	parser     *Parser
	reorderer  *Reorderer
	reassembler *Reassembler

	mu          sync.Mutex
	parseErrors int
}

// NewPipeline assembles the stages with validated bounds.
func NewPipeline(maxPacketSize, window int, reorderTimeout time.Duration) *Pipeline {
	return &Pipeline{
		parser:      NewParser(maxPacketSize),
		reorderer:   NewReorderer(window, reorderTimeout),
		reassembler: NewReassembler(),
	}
}

// Ingest parses one raw datagram, feeds valid packets through the reorderer
// and reassembles frames from the ordered output. Parse failures are counted
// and skipped; they never stop the stream.
func (p *Pipeline) Ingest(raw []byte, now time.Time) error {
	pkt, err := p.parser.Parse(raw)
	if err != nil {
		p.mu.Lock()
		p.parseErrors++
		p.mu.Unlock()
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	ordered := p.reorderer.Push(pkt, now)
	for _, op := range ordered {
		p.reassembler.Push(op)
	}
	return nil
}

// Tick applies the reorder timeout and feeds any flushed packets into frame
// reassembly.
func (p *Pipeline) Tick(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ordered := p.reorderer.Tick(now)
	for _, op := range ordered {
		p.reassembler.Push(op)
	}
}

// Flush ends a stream cleanly: remaining reorder holes are registered as gaps
// and any frame left without a marker is reported incomplete.
func (p *Pipeline) Flush(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ordered := p.reorderer.Flush(now)
	for _, op := range ordered {
		p.reassembler.Push(op)
	}
	p.reassembler.FlushIncomplete()
}

// ParseErrors reports how many datagrams failed parsing.
func (p *Pipeline) ParseErrors() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.parseErrors
}

// Stats returns a consistent snapshot of all counters.
func (p *Pipeline) Stats() PipelineStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	rs := p.reorderer.Stats()
	fs := p.reassembler.Stats()
	return PipelineStats{
		ParseErrors:      p.parseErrors,
		Received:         rs.Received,
		Duplicates:       rs.Duplicates,
		LatePackets:      rs.LatePackets,
		MissingPackets:   rs.MissingPackets,
		GapEvents:        rs.GapEvents,
		TimeoutFlushes:   rs.TimeoutFlushes,
		CompleteFrames:   fs.CompleteFrames,
		IncompleteFrames: fs.IncompleteFrames,
	}
}

// Reset clears every stage so a disconnect cannot leak packets or frames
// into the next run. Open frames are flushed as incomplete first.
func (p *Pipeline) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reassembler.Reset()
	p.reorderer.Reset()
	p.parseErrors = 0
}
