// Package rtp reorders and deduplicates RTP packets using 16-bit sequence
// wrap-around. Loss and out-of-window late packets are recorded as gaps.
package rtp

import (
	"fmt"

	"github.com/pion/rtp"

	"mediacheck/internal/gap"
	"mediacheck/internal/timebase"
)

// Config configures one track's sequence window and sample mapping.
type Config struct {
	Track            string
	SampleRate       uint32
	StartSequence    uint16
	Window           uint16
	SamplesPerPacket int64
	Ledger           *gap.Ledger
}

// TimedPacket is an ordered RTP packet with unwrapped sequence and sample axis.
type TimedPacket struct {
	Packet        *rtp.Packet
	ExtSeq        int64
	StartSample   int64
	SampleCount   int64
	Discontinuity bool
}

// PushResult reports how one input packet was handled and any packets newly
// released in sequence order.
type PushResult struct {
	ExtSeq    int64
	Duplicate bool
	Late      bool
	Ready     []TimedPacket
}

// Reorderer is a bounded sequence window plus an ordered release frontier.
type Reorderer struct {
	cfg      Config
	ledger   *gap.Ledger
	window   int64
	startExt int64
	nextExt  int64
	highExt  int64
	seen     map[int64]struct{}
	packets  map[int64]*rtp.Packet
}

func NewReorderer(cfg Config) (*Reorderer, error) {
	if cfg.SampleRate == 0 {
		return nil, timebase.NewError(timebase.CodeInvalidArgument, "rtp sample rate required")
	}
	if cfg.SamplesPerPacket <= 0 {
		return nil, timebase.NewError(timebase.CodeInvalidArgument, "samples per packet must be positive and uniform")
	}
	window := int64(cfg.Window)
	if window <= 0 {
		window = 64
	}
	ledger := cfg.Ledger
	if ledger == nil {
		ledger = gap.NewLedger(cfg.Track, cfg.SampleRate)
	}
	start := int64(cfg.StartSequence)
	return &Reorderer{
		cfg:      cfg,
		ledger:   ledger,
		window:   window,
		startExt: start,
		nextExt:  start,
		highExt:  start,
		seen:     make(map[int64]struct{}),
		packets:  make(map[int64]*rtp.Packet),
	}, nil
}

func (r *Reorderer) Ledger() *gap.Ledger { return r.ledger }

// extend16 extends seq relative to reference using RFC-style signed delta.
func extend16(seq uint16, reference int64) int64 {
	ref16 := uint16(reference)
	delta := int16(seq - ref16)
	return reference + int64(delta)
}

// PushRaw parses an RTP packet with pion/rtp before applying Push.
func (r *Reorderer) PushRaw(raw []byte) (PushResult, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return PushResult{}, timebase.NewError(timebase.CodeReorder, "invalid RTP packet").Wrap(err)
	}
	return r.Push(&pkt)
}

// Push accepts one parsed packet. Duplicates are reported, not fatal.
func (r *Reorderer) Push(pkt *rtp.Packet) (PushResult, error) {
	if pkt == nil {
		return PushResult{}, timebase.NewError(timebase.CodeInvalidArgument, "nil RTP packet")
	}
	ext := extend16(pkt.SequenceNumber, r.highExt)
	res := PushResult{ExtSeq: ext}

	if _, ok := r.seen[ext]; ok {
		res.Duplicate = true
		return res, nil
	}
	r.seen[ext] = struct{}{}
	if ext > r.highExt {
		r.highExt = ext
	}

	// A packet behind the release frontier can no longer be inserted.
	if ext < r.nextExt || r.highExt-ext > r.window {
		startSample := (ext - r.startExt) * r.cfg.SamplesPerPacket
		if startSample < 0 {
			startSample = 0
		}
		g, err := r.ledger.RecordLate(ext, startSample, int(r.cfg.SamplesPerPacket),
			fmt.Sprintf("sequence %d arrived outside reorder window %d", pkt.SequenceNumber, r.window))
		if err != nil {
			return res, err
		}
		_ = g
		res.Late = true
		r.pruneSeen()
		return res, nil
	}

	r.packets[ext] = pkt
	ready, err := r.pump(false)
	if err != nil {
		return res, err
	}
	res.Ready = ready
	r.pruneSeen()
	return res, nil
}

func (r *Reorderer) pruneSeen() {
	floor := r.nextExt - r.window
	for ext := range r.seen {
		if ext < floor {
			delete(r.seen, ext)
		}
	}
}

// pump releases contiguous packets. A missing sequence run is skipped once
// its leading edge is older than the window, or immediately on flush.
func (r *Reorderer) pump(force bool) ([]TimedPacket, error) {
	var out []TimedPacket
	for r.nextExt <= r.highExt {
		pkt, ok := r.packets[r.nextExt]
		if ok {
			out = append(out, TimedPacket{
				Packet:      pkt,
				ExtSeq:      r.nextExt,
				StartSample: (r.nextExt - r.startExt) * r.cfg.SamplesPerPacket,
				SampleCount: r.cfg.SamplesPerPacket,
			})
			delete(r.packets, r.nextExt)
			r.nextExt++
			continue
		}

		if !force && r.highExt-r.nextExt < r.window {
			break
		}

		end := r.nextExt
		for end <= r.highExt {
			if _, present := r.packets[end]; present {
				break
			}
			end++
		}
		lastMissing := end - 1
		count := int(lastMissing-r.nextExt) + 1
		startSample := (r.nextExt - r.startExt) * r.cfg.SamplesPerPacket
		endSample := (lastMissing + 1 - r.startExt) * r.cfg.SamplesPerPacket
		if _, err := r.ledger.RecordMissing(r.nextExt, lastMissing, count, startSample, endSample,
			"sequence gap after reorder window"); err != nil {
			return out, err
		}
		r.nextExt = lastMissing + 1
	}
	if force {
		// Caller has supplied every packet; nothing may remain buffered.
		for ext := range r.packets {
			if ext >= r.nextExt {
				// Should be unreachable because the loop skips all gaps.
				r.nextExt = ext
			}
		}
	}
	return out, nil
}

// Flush closes all remaining sequence gaps and returns packets in order.
func (r *Reorderer) Flush() ([]TimedPacket, error) {
	ready, err := r.pump(true)
	if err != nil {
		return nil, err
	}
	// pump's forced gap skips make this sorted drain deterministic.
	for {
		more, err := r.pump(true)
		if err != nil {
			return nil, err
		}
		if len(more) == 0 {
			break
		}
		ready = append(ready, more...)
	}
	return ready, nil
}

// OrderedAll is a convenience for finite batches: push every packet, flush,
// and return the complete ordered release plus the shared ledger.
func OrderedAll(cfg Config, packets []*rtp.Packet) ([]TimedPacket, *gap.Ledger, error) {
	r, err := NewReorderer(cfg)
	if err != nil {
		return nil, nil, err
	}
	if cfg.Ledger != nil {
		r.ledger = cfg.Ledger
	}
	var out []TimedPacket
	for _, p := range packets {
		res, err := r.Push(p)
		if err != nil {
			return nil, r.ledger, err
		}
		out = append(out, res.Ready...)
	}
	rest, err := r.Flush()
	if err != nil {
		return nil, r.ledger, err
	}
	out = append(out, rest...)
	return out, r.ledger, nil
}
