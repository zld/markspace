// Package receive chains wire decoding, the sequence window and the
// jitter controller into a playout pipeline shared by the simulator
// and the UDP receiver.
package receive

import (
	"streamlab/internal/jitter"
	"streamlab/internal/wire"
)

// Event is the outcome of one playout tick.
type Event struct {
	Seq       uint16
	TimeMs    int64
	Concealed bool
	Watermark int     // buffered frames ahead of the cursor
	Energy    float64 // output frame energy
	LatencyMs float64 // playout time minus frame-clock send time
}

// Pipeline consumes datagrams and produces playout events on a frame
// clock. It is driven explicitly (virtual or real time) and never
// blocks or spawns goroutines.
type Pipeline struct {
	ctl      *jitter.Controller
	win      *jitter.Window
	conc     *Concealer
	frameMS  int64
	streamID uint32
	spf      int // samples per frame

	Received int
	Dropped  map[string]int
	Events   []Event

	nextPlay uint16
	started  bool
	playAt   int64
	offset   int64 // receive clock minus frame clock, fixed at first packet
}

// NewPipeline wires the shared controller, window and concealer.
func NewPipeline(frameMS int64, streamID uint32, spf int, ctlCfg jitter.Config, winSize, maxConceal int) *Pipeline {
	return &Pipeline{
		ctl:      jitter.NewController(ctlCfg),
		win:      jitter.NewWindow(winSize),
		conc:     NewConcealer(maxConceal),
		frameMS:  frameMS,
		streamID: streamID,
		spf:      spf,
		Dropped:  make(map[string]int),
	}
}

// Controller exposes the shared adaptive controller.
func (p *Pipeline) Controller() *jitter.Controller { return p.ctl }

// Concealer exposes the concealment metrics collector.
func (p *Pipeline) Concealer() *Concealer { return p.conc }

// OnDatagram decodes, validates and buffers one datagram. Bad packets
// are counted by category and never interrupt the receive loop.
func (p *Pipeline) OnDatagram(data []byte, arrivalMs int64) {
	pkt, err := wire.Decode(data, p.streamID, p.frameMS)
	if err != nil {
		p.Dropped[wire.Category(err)]++
		return
	}
	p.Received++
	p.ctl.Observe(arrivalMs, pkt.Timestamp)
	if !p.started {
		p.started = true
		p.nextPlay = pkt.Seq
		p.offset = arrivalMs - int64(pkt.Seq)*p.frameMS
		p.playAt = arrivalMs + int64(p.ctl.TargetLevel())*p.frameMS
	}
	p.win.SetPlayed(p.nextPlay)
	if reason := p.win.Insert(pkt.Seq, pkt.Samples); reason != "" {
		p.Dropped[reason]++
	}
}

// AdvanceTo emits playout events for every frame due by nowMs. On an
// underrun the frame is concealed and the playout clock slips one
// frame to rebuild the buffer.
func (p *Pipeline) AdvanceTo(nowMs int64) {
	if !p.started {
		return
	}
	for p.playAt <= nowMs {
		ev := Event{Seq: p.nextPlay, TimeMs: p.playAt}
		if samples, ok := p.win.Pop(p.nextPlay); ok {
			p.conc.Real(samples)
			ev.Energy = Energy(samples)
			ev.LatencyMs = float64(p.playAt - (p.offset + int64(p.nextPlay)*p.frameMS))
		} else {
			out := p.conc.Conceal(p.spf)
			ev.Concealed = true
			ev.Energy = Energy(out)
			p.playAt += p.frameMS // slip to rebuild buffering
		}
		p.nextPlay++
		p.win.SetPlayed(p.nextPlay)
		ev.Watermark = p.win.Depth(p.nextPlay)
		p.Events = append(p.Events, ev)
		p.playAt += p.frameMS
	}
}
