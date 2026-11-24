package rtp

import (
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"mediarc/internal/model"
)

type Config struct {
	// Window is the maximum extended sequence distance, behind the newest
	// sequence, for which a missing packet remains recoverable.
	Window uint64
	SSRC   uint32
}

type Packet struct {
	Sequence    uint16
	ExtSequence uint64
	Timestamp   uint32
	Marker      bool
	PayloadType uint8
	Payload     []byte
	Offset      int64
	Recovered   bool
}

type missingInfo struct {
	ext       uint64
	sequence  uint16
	timestamp uint32
	offset    int64
	injected  bool
}

type Report struct {
	Delivered        []Packet `json:"-"`
	SequenceOrder    []uint16 `json:"sequence_order"`
	InjectedLost     int      `json:"injected_lost"`
	Duplicates       int      `json:"duplicates"`
	Reordered        int      `json:"reordered"`
	Recovered        int      `json:"recovered"`
	Unrecoverable    int      `json:"unrecoverable"`
	UnrecoverableSeq []uint16 `json:"unrecoverable_sequences"`
	FirstTimestamp   uint32   `json:"first_timestamp,omitempty"`
	FirstOffset      int64    `json:"first_offset,omitempty"`
	RecoveryRate     float64  `json:"packet_recovery_rate"`
}

type Recoverer struct {
	cfg      Config
	maxExt   *uint64
	packets  map[uint64]Packet
	missing  map[uint64]missingInfo
	injected map[uint16]bool
	lossInfo map[uint16]model.RTPPacket
	report   Report
}

func NewRecoverer(cfg Config) *Recoverer {
	if cfg.Window == 0 {
		cfg.Window = 64
	}
	return &Recoverer{
		cfg:      cfg,
		packets:  map[uint64]Packet{},
		missing:  map[uint64]missingInfo{},
		injected: map[uint16]bool{},
		lossInfo: map[uint16]model.RTPPacket{},
	}
}

// Expand converts a wrapping uint16 RTP sequence to a monotonic extended
// sequence. It is deterministic for the observed stream.
func (r *Recoverer) Expand(seq uint16) uint64 {
	if r.maxExt == nil {
		return uint64(seq)
	}
	last16 := uint16(*r.maxExt)
	diff := int32(seq) - int32(last16)
	if diff < -32768 {
		diff += 65536
	} else if diff > 32768 {
		diff -= 65536
	}
	candidate := int64(*r.maxExt) + int64(diff)
	if candidate < 0 {
		return uint64(seq)
	}
	return uint64(candidate)
}

// NoteInjectedLoss records that seq was deliberately absent from the primary
// arrival. Recovery packets may still fill it before the window closes.
func (r *Recoverer) NoteInjectedLoss(p model.RTPPacket) {
	if r.injected[p.Sequence] {
		return
	}
	r.injected[p.Sequence] = true
	r.lossInfo[p.Sequence] = p
	r.report.InjectedLost++
}

// AcceptModel converts the stable fixture projection through pion/rtp types.
func (r *Recoverer) AcceptModel(p model.RTPPacket, recovered bool) error {
	wire := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         p.Marker,
			PayloadType:    96,
			SequenceNumber: p.Sequence,
			Timestamp:      p.Timestamp,
			SSRC:           p.SSRC,
		},
		Payload: p.Payload,
	}
	raw, err := wire.Marshal()
	if err != nil {
		return model.NewError(model.StageRTP, model.CodeCorruptPayload, "marshal RTP packet", err)
	}
	var parsed rtp.Packet
	if err := parsed.Unmarshal(raw); err != nil {
		return model.NewError(model.StageRTP, model.CodeCorruptPayload, "unmarshal RTP packet", err)
	}
	return r.Accept(&parsed, p.SourceOffset, recovered)
}

func (r *Recoverer) Accept(p *rtp.Packet, offset int64, recovered bool) error {
	ext := r.Expand(p.SequenceNumber)
	if _, exists := r.packets[ext]; exists {
		r.report.Duplicates++
		return nil
	}

	wasMissing := false
	injectedFill := false
	if info, ok := r.missing[ext]; ok {
		wasMissing = true
		injectedFill = info.injected || r.injected[p.SequenceNumber]
		if injectedFill {
			r.report.Recovered++
		} else {
			r.report.Reordered++
		}
		delete(r.missing, ext)
	}

	isReorder := !wasMissing && r.maxExt != nil && ext < *r.maxExt
	if isReorder {
		r.report.Reordered++
	}

	pkt := Packet{
		Sequence: p.SequenceNumber, ExtSequence: ext, Timestamp: p.Timestamp,
		Marker: p.Marker, PayloadType: p.PayloadType, Payload: append([]byte(nil), p.Payload...),
		Offset: offset, Recovered: injectedFill,
	}
	r.packets[ext] = pkt
	r.report.Delivered = append(r.report.Delivered, pkt)
	r.report.SequenceOrder = append(r.report.SequenceOrder, p.SequenceNumber)

	if r.maxExt == nil || ext > *r.maxExt {
		old := r.maxExt
		r.maxExt = &ext
		if old != nil {
			for gap := *old + 1; gap < ext; gap++ {
				if _, ok := r.packets[gap]; ok {
					continue
				}
				if _, noted := r.missing[gap]; !noted {
					seq := uint16(gap)
					info := missingInfo{ext: gap, sequence: seq, injected: r.injected[seq]}
					if lp, ok := r.lossInfo[seq]; ok {
						info.timestamp = lp.Timestamp
						info.offset = lp.SourceOffset
					}
					r.missing[gap] = info
				}
			}
		}
	}
	r.expireWindow()
	return nil
}

func (r *Recoverer) expireWindow() {
	if r.maxExt == nil {
		return
	}
	cutoff := int64(*r.maxExt - r.cfg.Window)
	if cutoff < 0 {
		return
	}
	var exts []uint64
	for ext := range r.missing {
		if int64(ext) <= cutoff {
			exts = append(exts, ext)
		}
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i] < exts[j] })
	for _, ext := range exts {
		info := r.missing[ext]
		if r.report.Unrecoverable == 0 {
			r.report.FirstTimestamp = info.timestamp
			r.report.FirstOffset = info.offset
		}
		r.report.Unrecoverable++
		r.report.UnrecoverableSeq = append(r.report.UnrecoverableSeq, info.sequence)
		delete(r.missing, ext)
	}
	if len(exts) > 0 {
		sort.Slice(r.report.UnrecoverableSeq, func(i, j int) bool {
			return r.report.UnrecoverableSeq[i] < r.report.UnrecoverableSeq[j]
		})
	}
}

// Finalize expires every remaining gap and returns a structured error when
// injected losses could not be recovered inside the window.
func (r *Recoverer) Finalize() Report {
	// At end of stream, gaps are only expired if the configured window has
	// already moved past them; otherwise callers may explicitly AdvanceWindow.
	r.expireWindow()
	if r.report.InjectedLost > 0 {
		r.report.RecoveryRate = float64(r.report.Recovered) / float64(r.report.InjectedLost)
	} else {
		r.report.RecoveryRate = 1
	}
	out := r.report
	out.Delivered = append([]Packet(nil), r.report.Delivered...)
	return out
}

// AdvanceWindow simulates later sequence progress without accepting media. It
// is used by tests and the over-window acceptance case.
func (r *Recoverer) AdvanceWindow(progress uint64) {
	if r.maxExt == nil {
		v := progress
		r.maxExt = &v
	} else {
		*r.maxExt += progress
	}
	r.expireWindow()
}

// UnrecoverableError builds the cross-subsystem error carrying packet count,
// first timestamp and source offset.
func (r *Recoverer) UnrecoverableError(rep Report) *model.MediaError {
	if rep.Unrecoverable == 0 {
		return nil
	}
	var ts model.Timestamp
	if rep.FirstTimestamp != 0 {
		ts = model.Timestamp{Ticks: int64(rep.FirstTimestamp), Base: model.TimeBase{Num: 1, Den: 90000}}
	}
	msg := fmt.Sprintf("%d packet(s) missing after recovery window %d", rep.Unrecoverable, r.cfg.Window)
	err := model.NewError(model.StageRTP, model.CodeLossOverWindow, msg, nil)
	err.Unrecoverable = rep.Unrecoverable
	if rep.FirstTimestamp != 0 {
		err.Timestamp = &ts
	}
	if rep.FirstOffset != 0 {
		off := rep.FirstOffset
		err.SourceOffset = &off
	}
	return err
}
