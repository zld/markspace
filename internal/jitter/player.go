package jitter

import (
	"time"

	"jbbench/internal/conceal"
	"jbbench/internal/protocol"
)

// Outcome is one frame produced by the shared playback pipeline.
type Outcome struct {
	FrameNo   uint32
	At        time.Duration // play time on the receive clock
	Latency   time.Duration // receive-time playout latency; 0 for concealed
	Concealed bool
	Frame     conceal.Frame
}

// PlayerConfig wires the window, controller and concealer together.
type PlayerConfig struct {
	WindowCapacity int
	DropPolicy     DropPolicy
	Controller     ControllerConfig
	Concealment    conceal.Config
}

// DefaultPlayerConfig returns the shared defaults used by both receive paths.
func DefaultPlayerConfig() PlayerConfig {
	return PlayerConfig{
		WindowCapacity: 64,
		DropPolicy:     DropRejectNew,
		Controller:     DefaultControllerConfig(),
		Concealment:    conceal.DefaultConfig(),
	}
}

// Player is the single buffer-control strategy shared by the live UDP path
// and the offline replay path. Both call Push for decoded packets and Drain
// for frames due on their respective clocks.
type Player struct {
	window     *ReorderWindow
	controller *Controller
	concealer  *conceal.Concealer
	frameNS    time.Duration

	anchorRecv time.Duration
	anchorSend time.Time
	anchored   bool

	lastSeq    uint16
	hasSeq     bool
	reordered  uint64
	received   uint64
	played     uint64
	concealed  uint64
	started    bool
	startFrame uint32
}

// NewPlayer constructs a pipeline from the given configuration.
func NewPlayer(cfg PlayerConfig) *Player {
	if cfg.WindowCapacity < 1 {
		cfg.WindowCapacity = 64
	}
	frameNS := protocol.FrameDurationNS()
	return &Player{
		window:     NewReorderWindow(cfg.WindowCapacity, cfg.DropPolicy),
		controller: NewController(cfg.Controller),
		concealer:  conceal.New(cfg.Concealment),
		frameNS:    frameNS,
	}
}

// Controller exposes the shared controller for inspection and testing.
func (p *Player) Controller() *Controller { return p.controller }

// Window exposes the reorder window.
func (p *Player) Window() *ReorderWindow { return p.window }

// Concealer exposes the PLC state.
func (p *Player) Concealer() *conceal.Concealer { return p.concealer }

// Push processes one decoded, checksum-valid packet. The reason is "" when the
// frame enters the window; otherwise it is "duplicate", "late" or "overflow".
func (p *Player) Push(pkt protocol.Packet, arrival time.Duration) (reason string) {
	if p.hasSeq {
		d, amb := protocol.SeqCompare(pkt.Seq, p.lastSeq)
		if amb {
			return "ambiguous"
		}
		if d < 0 {
			p.reordered++
		}
	}
	ok, reason := p.window.Push(Frame{Pkt: pkt, Arrival: arrival})
	if !ok {
		return reason
	}
	p.hasSeq = true
	p.lastSeq = pkt.Seq
	p.received++
	p.controller.ObserveArrival(arrival)
	return ""
}

// ReorderedCount returns accepted packets that arrived behind the last seq.
func (p *Player) ReorderedCount() uint64 { return p.reordered }

// ReceivedCount counts valid packets that entered the window.
func (p *Player) ReceivedCount() uint64 { return p.received }

// SetClockAnchor maps sender timestamps to the receive clock once.
func (p *Player) SetClockAnchor(recv time.Duration, sendNS uint64) {
	p.anchorRecv = recv
	p.anchorSend = time.Unix(0, int64(sendNS))
	p.anchored = true
}

func (p *Player) playTime(frameNo uint32) time.Duration {
	if !p.anchored {
		return 0
	}
	sendAt := time.Unix(0, int64(frameNo)*int64(p.frameNS))
	offset := sendAt.Sub(p.anchorSend)
	return p.anchorRecv + offset
}

// PrerollReady reports whether buffering has reached the target watermark.
func (p *Player) PrerollReady() bool {
	return p.window.Len() >= p.controller.LevelFrames()
}

// Drain emits every frame due at receive-clock time now. Concealed frames are
// produced for missing cursor frames; the returned slice is in play order.
// The cursor is never advanced past the highest frame number seen so far.
func (p *Player) Drain(now time.Duration) []Outcome {
	var out []Outcome
	for p.window.Cursor() <= p.window.High() {
		cursor := p.window.Cursor()
		due := p.playTime(cursor)
		if !p.anchored || due > now {
			return out
		}
		f, ok := p.window.Pop()
		var o Outcome
		o.FrameNo = cursor
		o.At = due
		if ok {
			o.Frame = p.concealer.Real(f.Pkt.Samples)
			// Network latency on the receive clock: actual arrival instant
			// minus the sender's nominal send instant mapped to that clock.
			sendOnRecv := p.anchorRecv +
				time.Duration(int64(f.Pkt.FrameNo)*int64(p.frameNS)) -
				time.Duration(int64(p.anchorSend.UnixNano()))
			o.Latency = clampNonNeg(f.Arrival - sendOnRecv)
			if !p.started {
				p.started = true
				p.startFrame = cursor
			}
		} else {
			o.Frame = p.concealer.Missing()
			o.Concealed = true
			p.window.Skip()
			p.concealed++
		}
		o.Frame.Energy = conceal.Energy(o.Frame.Samples)
		p.played++
		out = append(out, o)
	}
	return out
}

// FlushRemaining plays out every buffered frame after input exhaustion,
// advancing the logical clock frame by frame. Missing cursor frames are
// concealed; the returned gap count is the number of skips performed. The
// cursor never advances past the highest frame number ever seen.
func (p *Player) FlushRemaining(now time.Duration) ([]Outcome, int) {
	var out []Outcome
	gaps := 0
	// Nothing to flush if no frame ever entered the window.
	if !p.window.started {
		return out, gaps
	}
	end := p.window.High() + 1
	for p.window.Cursor() < end {
		now += p.frameNS
		before := p.concealed
		out = append(out, p.Drain(now)...)
		gaps += int(p.concealed - before)
	}
	return out, gaps
}

// Played and Concealed totals.
func (p *Player) PlayedCount() uint64    { return p.played }
func (p *Player) ConcealedCount() uint64 { return p.concealed }

// LevelFrames returns the current controller watermark in frames.
func (p *Player) LevelFrames() int { return p.controller.LevelFrames() }

func clampNonNeg(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}
