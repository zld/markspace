// Package stream turns ordered RTP packets into a verifiable structural
// description: tracks, codec parameters, sample time ranges, key-frame media
// times and key-frame intervals measured both in samples and in rational
// time. It shares the time mapper and fault model with damage localization.
package stream

import (
	"media-check/internal/clock"
	"media-check/internal/fault"
	"media-check/internal/fixture"
	"media-check/internal/rtcp"
	"media-check/internal/rtp"
)

// Sample is one parseable media unit placed on the unified time base.
type Sample struct {
	TrackID   uint32
	UnitIndex uint32
	IsKey     bool
	// SeqStart/SeqEnd are the original extended RTP sequence span of the
	// unit (the fixture uses one packet per unit).
	SeqStart uint64
	SeqEnd   uint64
	// Time is the sample's media time on the rational time base.
	Time clock.Rational
	// Size is the payload size in bytes as received on the wire.
	Size int
}

// KeyInterval is the gap between two successive key frames.
type KeyInterval struct {
	TrackID uint32
	// FromIndex/ToIndex are media-unit indices of the two key frames.
	FromIndex uint32
	ToIndex   uint32
	Samples   int64
	Duration  clock.Rational
	// SeqStart/SeqEnd cover the original packet sequence range of the span.
	SeqStart uint64
	SeqEnd   uint64
	// Anomalous marks intervals whose sample count differs from the nominal
	// key period (e.g. a loss burst swallowing an expected key frame).
	Anomalous bool
}

// TrackMeta binds an SSRC to its logical track identity and parameters.
type TrackMeta struct {
	TrackID   uint32
	Kind      fixture.TrackKind
	ClockRate uint32
	// KeyEvery is the configured nominal key-frame period in samples
	// (0 for audio); used to judge whether an observed interval is anomalous.
	KeyEvery int
}

// TrackReport describes one track's parsed structure.
type TrackReport struct {
	TrackID         uint32
	Kind            fixture.TrackKind
	SSRC            uint32
	PayloadType     uint8
	ClockRate       uint32
	BodyLen         uint16
	KeyEvery        int
	SampleTimeRange [2]clock.Rational
	Samples         []Sample
	KeyTimes        []clock.Rational
	KeyIndices      []uint32
	Intervals       []KeyInterval
}

// Report is the full structural analysis result.
type Report struct {
	Tracks []*TrackReport
}

// Inspector consumes ordered packets and records parse faults itself so the
// damage locator later consumes the exact same fault model.
type Inspector struct {
	mapper          *rtcp.TimeMapper
	faults          *fault.Model
	meta            map[uint32]TrackMeta
	nominalKeyEvery map[uint32]int64
}

// NewInspector wires the shared mapper, fault model and SSRC->track table.
func NewInspector(mapper *rtcp.TimeMapper, fm *fault.Model, meta map[uint32]TrackMeta) *Inspector {
	nominal := make(map[uint32]int64, len(meta))
	for _, m := range meta {
		if m.KeyEvery > 0 {
			nominal[m.TrackID] = int64(m.KeyEvery)
		}
	}
	return &Inspector{
		mapper:          mapper,
		faults:          fm,
		meta:            meta,
		nominalKeyEvery: nominal,
	}
}

// Inspect consumes fully ordered packets and returns the structural report.
// A payload that fails header parsing or checksum verification records a
// fault and is skipped — never replaced with fabricated media — while
// scanning continues over the recoverable packets.
func (in *Inspector) Inspect(ordered []rtp.OrderedPacket) (*Report, error) {
	tracks := make(map[uint32]*TrackReport)
	var order []uint32
	for _, op := range ordered {
		p := op.Packet
		meta, ok := in.meta[p.SSRC]
		if !ok {
			in.faults.Record(fault.Fault{
				Code:     fault.CodeCorruptPayload,
				Severity: fault.SeverityFatal,
				Message:  "packet from unknown SSRC; no track mapping",
			})
			continue
		}
		tr, exists := tracks[p.SSRC]
		if !exists {
			tr = &TrackReport{
				TrackID:     meta.TrackID,
				Kind:        meta.Kind,
				SSRC:        p.SSRC,
				PayloadType: p.PayloadType,
				ClockRate:   meta.ClockRate,
			}
			tracks[p.SSRC] = tr
			order = append(order, p.SSRC)
		}
		in.scanPacket(tr, op)
	}
	rep := &Report{}
	for _, id := range order {
		tr := tracks[id]
		in.finishTrack(tr)
		rep.Tracks = append(rep.Tracks, tr)
	}
	return rep, nil
}

func (in *Inspector) scanPacket(tr *TrackReport, op rtp.OrderedPacket) {
	p := op.Packet
	payload := p.Payload
	hdr, firstOff, err := fixture.ParseMediaHeader(payload)
	if err != nil {
		in.faults.Record(fault.Fault{
			Code:           fault.CodeTruncated,
			Severity:       fault.SeverityFatal,
			TrackID:        tr.TrackID,
			FirstErrOffset: firstOff,
			SeqStart:       op.ExtSeq,
			SeqEnd:         op.ExtSeq,
			Message:        "truncated media payload: header incomplete",
			Recovered:      false,
		})
		return
	}
	tr.BodyLen = hdr.BodyLen
	wantLen := fixture.MediaHeaderLen + int(hdr.BodyLen)
	if len(payload) < wantLen {
		in.faults.Record(fault.Fault{
			Code:           fault.CodeTruncated,
			Severity:       fault.SeverityFatal,
			TrackID:        tr.TrackID,
			FirstErrOffset: int64(len(payload)),
			SeqStart:       op.ExtSeq,
			SeqEnd:         op.ExtSeq,
			Message:        "payload truncated: declared body length exceeds bytes received",
			Recovered:      false,
		})
		return
	}
	if !fixture.VerifyChecksum(payload) {
		in.faults.Record(fault.Fault{
			Code:           fault.CodeCorruptPayload,
			Severity:       fault.SeverityFatal,
			TrackID:        tr.TrackID,
			FirstErrOffset: fixture.MediaHeaderLen,
			SeqStart:       op.ExtSeq,
			SeqEnd:         op.ExtSeq,
			Message:        "payload checksum mismatch",
			Recovered:      false,
		})
		return
	}
	tm, _ := in.mapper.Map(tr.TrackID, p.Timestamp)
	s := Sample{
		TrackID:   tr.TrackID,
		UnitIndex: hdr.UnitIndex,
		IsKey:     hdr.Flags&fixture.MediaFlagKey != 0,
		SeqStart:  op.ExtSeq,
		SeqEnd:    op.ExtSeq,
		Time:      tm,
		Size:      len(payload),
	}
	tr.Samples = append(tr.Samples, s)
	if s.IsKey {
		tr.KeyTimes = append(tr.KeyTimes, tm)
		tr.KeyIndices = append(tr.KeyIndices, s.UnitIndex)
	}
}

func (in *Inspector) finishTrack(tr *TrackReport) {
	if len(tr.Samples) == 0 {
		return
	}
	tr.SampleTimeRange[0] = tr.Samples[0].Time
	tr.SampleTimeRange[1] = tr.Samples[len(tr.Samples)-1].Time
	if len(tr.KeyIndices) < 2 {
		return
	}
	tr.KeyEvery = int(tr.KeyIndices[1] - tr.KeyIndices[0])
	nominal, hasNominal := in.nominalKeyEvery[tr.TrackID]
	if !hasNominal {
		// Fall back to the first observed interval when no config exists.
		nominal = int64(tr.KeyEvery)
	}
	for i := 1; i < len(tr.KeyIndices); i++ {
		from, to := tr.KeyIndices[i-1], tr.KeyIndices[i]
		samples := int64(to - from)
		iv := KeyInterval{
			TrackID:   tr.TrackID,
			FromIndex: from,
			ToIndex:   to,
			Samples:   samples,
			Duration:  tr.KeyTimes[i].Sub(tr.KeyTimes[i-1]),
			SeqStart:  tr.keySeq(from),
			SeqEnd:    tr.keySeq(to),
			Anomalous: samples != nominal,
		}
		tr.Intervals = append(tr.Intervals, iv)
	}
}

func (tr *TrackReport) keySeq(unit uint32) uint64 {
	for _, s := range tr.Samples {
		if s.UnitIndex == unit {
			return s.SeqStart
		}
	}
	return 0
}

// ByTrackID returns the report for a logical track id, or nil.
func (r *Report) ByTrackID(trackID uint32) *TrackReport {
	for _, tr := range r.Tracks {
		if tr.TrackID == trackID {
			return tr
		}
	}
	return nil
}

// VideoTrack returns the video track report or nil.
func (r *Report) VideoTrack() *TrackReport {
	for _, tr := range r.Tracks {
		if tr.Kind == fixture.TrackVideo {
			return tr
		}
	}
	return nil
}

// AudioTrack returns the audio track report or nil.
func (r *Report) AudioTrack() *TrackReport {
	for _, tr := range r.Tracks {
		if tr.Kind == fixture.TrackAudio {
			return tr
		}
	}
	return nil
}
