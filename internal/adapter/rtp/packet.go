// Package rtp wraps github.com/pion/rtp to parse RTP packets into the unified
// event model, including timestamp unwrapping, sequence validation and size
// bounds for the local QC fixtures.
package rtp

import (
	"fmt"

	"github.com/pion/rtp"

	"mediaqc/internal/adapter"
	"mediaqc/internal/clock"
	"mediaqc/internal/model"
)

// Parser implements adapter.Parser for the RTP subset.
type Parser struct {
	limit    adapter.Limits
	clockRate uint32
}

// NewParser builds a parser. clockRate is the RTP media clock (e.g. 90000).
func NewParser(limit adapter.Limits, clockRate uint32) *Parser {
	return &Parser{limit: limit, clockRate: clockRate}
}

// Descriptor implements adapter.Parser.
func (p *Parser) Descriptor() adapter.Capability {
	return adapter.Capability{
		Kind: Kind(), Name: "rtp-local", Version: "1.8.11",
		Features: []string{"fec-xor", "sequence-unwrap", "timestamp-unwrap"},
		Limit:    p.limit,
	}
}

// Supports implements adapter.Parser.
func (p *Parser) Supports(f string) bool { return p.Descriptor().Supports(f) }

// Kind returns the adapter kind.
func Kind() adapter.Kind { return adapter.KindRTP }

func init() { adapter.MustRegister(NewParser(adapter.DefaultLimits(), 90000)) }

// ParsePacket parses one raw RTP datagram. seqOut/timestampOut are the
// unwrapped absolute sequence/timestamp counters maintained by the caller.
func (p *Parser) ParsePacket(materialID, trackID string, kind model.TrackKind,
	raw []byte, seqWrap, tsWrap *clock.Wraparound) (model.Event, error) {
	if int64(len(raw)) > p.limit.MaxBytes {
		return model.Event{}, model.NewError(model.CodeConfig, model.StageParse, materialID, trackID,
			fmt.Sprintf("rtp packet %d bytes exceeds limit", len(raw)))
	}
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(raw); err != nil {
		return model.Event{}, model.Wrap(err, model.CodeTruncatedFragment, model.StageParse,
			materialID, trackID, "malformed RTP packet")
	}
	if p.clockRate == 0 {
		return model.Event{}, model.NewError(model.CodeZeroTimescale, model.StageParse,
			materialID, trackID, "RTP clock rate is zero")
	}
	tb, err := clock.New(int64(p.clockRate))
	if err != nil {
		return model.Event{}, model.Wrap(err, model.CodeZeroTimescale, model.StageParse, materialID, trackID, err.Error())
	}
	absSeq := seqWrap.Extend(uint64(pkt.SequenceNumber))
	absTS := tsWrap.Extend(uint64(pkt.Timestamp))
	ev := model.Event{
		MaterialID: materialID,
		TrackID:    trackID,
		Kind:       kind,
		Sequence:   uint64(absSeq),
		Timebase:   tb,
		Start:      tb.At(absTS),
		Duration:   tb.At(0),
		PTS:        tb.At(absTS),
		Payload:    pkt.Payload,
		Size:       len(raw),
		Origin:     fmt.Sprintf("ssrc=%d seq=%d", pkt.SSRC, pkt.SequenceNumber),
	}
	return ev, nil
}

// ParseStream parses an ordered slice of datagrams and validates sequence
// continuity. Duplicate raw sequence numbers are reported with
// CodeDuplicateSeq; missing sequences are returned as declared Gaps but the
// stream is still emitted so the jitter buffer / FEC layers can act.
func (p *Parser) ParseStream(materialID, trackID string, kind model.TrackKind,
	datagrams [][]byte) ([]model.Event, []model.Gap, []error) {
	seqWrap, err := clock.NewWraparound(16)
	if err != nil {
		return nil, nil, []error{model.Wrap(err, model.CodeConfig, model.StageParse, materialID, trackID, "seq unwrap")}
	}
	tsWrap, err := clock.NewWraparound(32)
	if err != nil {
		return nil, nil, []error{model.Wrap(err, model.CodeConfig, model.StageParse, materialID, trackID, "ts unwrap")}
	}
	var (
		events []model.Event
		gaps   []model.Gap
		errs   []error
		seen   = map[uint16]uint64{}
		expect uint64
		have   uint64
	)
	for i, dg := range datagrams {
		ev, err := p.ParsePacket(materialID, trackID, kind, dg, seqWrap, tsWrap)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		// Extract the raw 16-bit sequence for duplicate detection.
		rawPkt := &rtp.Packet{}
		_ = rawPkt.Unmarshal(dg)
		if prev, dup := seen[rawPkt.SequenceNumber]; dup {
			errs = append(errs, model.NewError(model.CodeDuplicateSeq, model.StageParse, materialID, trackID,
				fmt.Sprintf("duplicate RTP sequence %d (events %d and %d)", rawPkt.SequenceNumber, prev, i)))
			continue
		}
		seen[rawPkt.SequenceNumber] = uint64(i)
		if have == 0 {
			expect = ev.Sequence
		}
		if ev.Sequence > expect {
			gaps = append(gaps, model.Gap{
				TrackID: trackID, FirstSeq: expect, LastSeq: ev.Sequence - 1, DeclaredBy: model.StageParse,
			})
		}
		expect = ev.Sequence + 1
		events = append(events, ev)
		have++
	}
	return events, gaps, errs
}
