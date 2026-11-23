package fixture

import (
	"encoding/binary"
	"fmt"

	"github.com/pion/rtp"
)

// FaultKind names a supported injection.
type FaultKind string

const (
	FaultTruncation     FaultKind = "truncation"
	FaultTfdtRegression FaultKind = "tfdt-regression"
	FaultIndexGap       FaultKind = "index-gap"
	FaultOversizeBox    FaultKind = "oversize-box"
	FaultSequenceGap    FaultKind = "mfhd-gap"
	FaultDuplicate      FaultKind = "duplicate-fragment"
	FaultCorruptMdat    FaultKind = "corrupt-mdat"
	FaultRTPSequenceGap FaultKind = "rtp-sequence-gap"
)

// AllFaults lists every injection kind in presentation order.
func AllFaults() []FaultKind {
	return []FaultKind{
		FaultTruncation,
		FaultTfdtRegression,
		FaultIndexGap,
		FaultOversizeBox,
		FaultSequenceGap,
		FaultDuplicate,
		FaultCorruptMdat,
		FaultRTPSequenceGap,
	}
}

// ParseFaults resolves a comma separated list of fault names.
func ParseFaults(list []string) ([]FaultKind, error) {
	if len(list) == 0 {
		return nil, nil
	}
	known := make(map[FaultKind]bool, len(AllFaults()))
	for _, f := range AllFaults() {
		known[f] = true
	}
	var out []FaultKind
	for _, raw := range list {
		for _, name := range splitComma(raw) {
			if name == "" {
				continue
			}
			f := FaultKind(name)
			if !known[f] {
				return nil, fmt.Errorf("fixture: unknown fault %q", name)
			}
			out = append(out, f)
		}
	}
	return out, nil
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

// anchor identifies which field of which final fragment a fault offset refers
// to. Offsets are resolved only after every fault has been applied, so a later
// rebuild cannot leave a stale byte position behind.
type anchor int

const (
	anchorNone anchor = iota
	anchorMoof
	anchorMdat
	anchorTfdt
	anchorTrunSizeField
	anchorSamplePayload
	anchorTruncation
)

// Injection records where a fault was applied, in logical and byte terms.
// ByteOffset is always the offset of the byte a correct parser must report.
type Injection struct {
	Kind       FaultKind `json:"kind"`
	TrackID    uint32    `json:"track_id,omitempty"`
	Fragment   int       `json:"fragment_index,omitempty"`
	Sequence   uint32    `json:"sequence,omitempty"`
	ByteOffset int64     `json:"byte_offset"`
	Detail     string    `json:"detail,omitempty"`

	anchor anchor
	cutAt  int64
}

// Anchor reports whether the injection offset was resolved against a field.
func (i Injection) Anchor() anchor { return i.anchor }

// InjectedStream is the damaged stream plus an exact description of the
// damage.
type InjectedStream struct {
	*Stream
	Injections []Injection `json:"injections"`
	// LogicalFragments is the fragment count before truncation removed the
	// tail of the file, i.e. what the input intended to carry.
	LogicalFragments int `json:"logical_fragments"`
	// TruncatedTo is the final file size when a truncation was applied.
	TruncatedTo int64 `json:"truncated_to,omitempty"`
}

// Inject applies the requested faults. Every fault is a pure transform of the
// baseline plan, applied in the order given; truncation is always last because
// it changes the file end. All recorded offsets are resolved against the final
// stream, so they equal the byte positions actually damaged.
func Inject(seed int64, tracks, fragments int, kinds []FaultKind) (*InjectedStream, error) {
	spec := DefaultSpec(seed, tracks, fragments)
	base, err := Build(spec)
	if err != nil {
		return nil, err
	}
	result := &InjectedStream{Stream: base}
	var truncate bool
	for _, kind := range kinds {
		if kind == FaultTruncation {
			truncate = true
			continue
		}
		plans, err := result.Plan()
		if err != nil {
			return nil, err
		}
		inj, next, err := applyFault(result.Stream, plans, kind)
		if err != nil {
			return nil, err
		}
		inj.Kind = kind
		result.Injections = append(result.Injections, inj)
		if next != nil {
			result.Stream = next
		}
	}
	if truncate {
		cut, idx := truncationPoint(result.Stream)
		inj := Injection{Kind: FaultTruncation, anchor: anchorTruncation, cutAt: cut, Fragment: idx}
		result.Data = result.Data[:cut]
		result.Digest = digest(result.Data)
		result.TruncatedTo = cut
		result.Injections = append(result.Injections, inj)
	}
	// The input intended to carry every fragment present before truncation;
	// duplicates are part of that count because they really are in the file.
	result.LogicalFragments = len(result.Stream.Fragments)
	// Resolve every anchor against the final layout. A truncation that removes
	// the bytes a fault was anchored to also removes that fault's observable
	// position, so the record is dropped rather than left pointing past EOF.
	kept := result.Injections[:0]
	for i := range result.Injections {
		inj := result.Injections[i]
		resolveAnchor(result.Stream, &inj)
		if inj.Kind != FaultTruncation && truncate && inj.ByteOffset >= int64(len(result.Data)) {
			continue
		}
		kept = append(kept, inj)
	}
	result.Injections = kept
	return result, nil
}

// truncationPoint cuts inside a moof body so its header stays complete and
// declares more bytes than remain. The returned index is the cut fragment.
func truncationPoint(s *Stream) (cut int64, fragment int) {
	if len(s.Fragments) == 0 {
		return int64(len(s.Data)), 0
	}
	// Cut two thirds of the way in so faults injected near the middle remain
	// observable in the surviving prefix.
	f := s.Fragments[(len(s.Fragments)*2)/3]
	return f.MoofOffset + 40, f.Index
}

func resolveAnchor(s *Stream, inj *Injection) {
	if inj.anchor == anchorNone {
		return
	}
	if inj.anchor == anchorTruncation {
		// The damaged bytes begin at the moof header whose declared size is
		// no longer backed by input. That header, not the cut position, is
		// what a parser must report.
		inj.ByteOffset = inj.cutAt
		f := fragmentAt(s, inj.Fragment)
		if f.MoofOffset > 0 {
			inj.ByteOffset = f.MoofOffset
		}
		inj.Detail = fmt.Sprintf("file truncated to %d bytes inside moof at %d", inj.cutAt, f.MoofOffset)
		return
	}
	f := fragmentAt(s, inj.Fragment)
	inj.TrackID, inj.Sequence = f.TrackID, f.Sequence
	switch inj.anchor {
	case anchorMoof:
		inj.ByteOffset = f.MoofOffset
	case anchorMdat:
		inj.ByteOffset = f.MdatOffset
	case anchorTfdt:
		inj.ByteOffset = f.TfdtOffset
	case anchorTrunSizeField:
		inj.ByteOffset = f.TrunOffset + 8 + 4 + 4 + 4 + int64(8*(len(f.Samples)-1)) + 4
	case anchorSamplePayload:
		// A consumer observes the gap when it reaches the sample, so the
		// reported position is where that RTP packet begins. The detail names
		// the sequence field inside it.
		if len(f.Samples) > 0 {
			inj.ByteOffset = f.Samples[len(f.Samples)-1].Offset
		}
	}
}

func fragmentAt(s *Stream, idx int) FragmentInfo {
	if idx < 0 || idx >= len(s.Fragments) {
		return FragmentInfo{}
	}
	return s.Fragments[idx]
}

func applyFault(base *Stream, plans []TrackPlan, kind FaultKind) (Injection, *Stream, error) {
	switch kind {
	case FaultTfdtRegression:
		return injectTfdtRegression(base, plans)
	case FaultIndexGap:
		return injectIndexGap(base)
	case FaultOversizeBox:
		return injectOversizeBox(base)
	case FaultSequenceGap:
		return injectSequenceGap(base, plans)
	case FaultDuplicate:
		return injectDuplicate(base, plans)
	case FaultCorruptMdat:
		return injectCorruptMdat(base)
	case FaultRTPSequenceGap:
		return injectRTPSequenceGap(base, plans)
	default:
		return Injection{}, nil, fmt.Errorf("fixture: unsupported fault %q", kind)
	}
}

// injectTfdtRegression rewinds the tfdt of one late fragment so it maps before
// its predecessor's end.
func injectTfdtRegression(base *Stream, plans []TrackPlan) (Injection, *Stream, error) {
	// Pick a fragment in the middle of the stream: late enough that a real
	// decode time exists, early enough to survive a truncation of the tail.
	victim := -1
	for i := len(plans) / 2; i < len(plans); i++ {
		if plans[i].Tfdt > 0 {
			victim = i
			break
		}
	}
	if victim < 0 {
		return Injection{anchor: anchorNone, Detail: "no stepping tfdt available"}, base, nil
	}
	regressed := plans[victim].Tfdt / 2
	plans[victim].Tfdt = regressed
	next, err := base.Reassemble(plans)
	if err != nil {
		return Injection{}, nil, err
	}
	inj := Injection{
		Fragment: victim,
		anchor:   anchorTfdt,
		Detail:   fmt.Sprintf("tfdt regressed to %d", regressed),
	}
	return inj, next, nil
}

// injectIndexGap shortens the sidx so later fragments are absent from the
// index but still present in the file.
func injectIndexGap(base *Stream) (Injection, *Stream, error) {
	if len(base.Fragments) < 2 {
		return Injection{anchor: anchorNone, Detail: "too few fragments"}, base, nil
	}
	covered := len(base.Fragments) / 2
	plans, err := base.Plan()
	if err != nil {
		return Injection{}, nil, err
	}
	next, err := Assemble(base.Spec, plans, covered)
	if err != nil {
		return Injection{}, nil, err
	}
	return Injection{
		Fragment: covered,
		anchor:   anchorMoof,
		Detail:   fmt.Sprintf("sidx covers %d of %d fragments", covered, len(next.Fragments)),
	}, next, nil
}

// injectOversizeBox rewrites one mdat size to run past the end of the file.
func injectOversizeBox(base *Stream) (Injection, *Stream, error) {
	if len(base.Fragments) == 0 {
		return Injection{anchor: anchorNone, Detail: "no fragments"}, base, nil
	}
	idx := len(base.Fragments) / 3
	f := base.Fragments[idx]
	data := append([]byte(nil), base.Data...)
	declared := int64(len(data)) - f.MdatOffset + 4096
	binary.BigEndian.PutUint32(data[f.MdatOffset:], uint32(declared))
	next := cloneStream(base, data)
	return Injection{
		Fragment: idx,
		anchor:   anchorMdat,
		Detail:   fmt.Sprintf("mdat declares %d bytes, %d available", declared, int64(len(data))-f.MdatOffset),
	}, next, nil
}

// injectSequenceGap drops one fragment so its mfhd number and decode time are
// missing from the stream.
func injectSequenceGap(base *Stream, plans []TrackPlan) (Injection, *Stream, error) {
	if len(plans) < 4 {
		return Injection{anchor: anchorNone, Detail: "too few fragments"}, base, nil
	}
	trackID := plans[0].Track.ID
	idx := -1
	for i := len(plans) / 2; i < len(plans); i++ {
		if plans[i].Track.ID == trackID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Injection{anchor: anchorNone, Detail: "no mid-track fragment"}, base, nil
	}
	dropped := plans[idx]
	plans = append(plans[:idx], plans[idx+1:]...)
	next, err := base.Reassemble(plans)
	if err != nil {
		return Injection{}, nil, err
	}
	observed := -1
	for i := idx; i < len(next.Fragments); i++ {
		if next.Fragments[i].TrackID == dropped.Track.ID {
			observed = i
			break
		}
	}
	if observed < 0 {
		return Injection{anchor: anchorNone, Detail: "no later fragment on the track"}, next, nil
	}
	return Injection{
		Fragment: observed,
		anchor:   anchorTfdt,
		Detail:   fmt.Sprintf("dropped fragment sequence %d; next is %d", dropped.Seq, next.Fragments[observed].Sequence),
	}, next, nil
}

// injectDuplicate re-emits one fragment immediately after itself, keeping the
// original mfhd sequence, tfdt and sample declarations.
func injectDuplicate(base *Stream, plans []TrackPlan) (Injection, *Stream, error) {
	if len(plans) < 2 {
		return Injection{anchor: anchorNone, Detail: "too few fragments"}, base, nil
	}
	idx := len(plans) / 2
	if idx%len(base.Spec.Tracks) != 0 {
		idx--
	}
	dup := plans[idx]
	dup.Samples = append([]PlanSample(nil), plans[idx].Samples...)
	plans = append(plans[:idx+1], append([]TrackPlan{dup}, plans[idx+1:]...)...)
	next, err := base.Reassemble(plans)
	if err != nil {
		return Injection{}, nil, err
	}
	return Injection{
		Fragment: idx + 1,
		anchor:   anchorMoof,
		Detail:   fmt.Sprintf("fragment %d repeated", idx),
	}, next, nil
}

// injectCorruptMdat forges a moof header at the end of the last sample's
// payload and inflates that sample's declared size past the mdat. A parser
// that resynchronises on box magic would report a phantom fragment; a
// size-driven parser reports the impossible sample range at the trun field.
func injectCorruptMdat(base *Stream) (Injection, *Stream, error) {
	if len(base.Fragments) == 0 {
		return Injection{anchor: anchorNone, Detail: "no fragments"}, base, nil
	}
	idx := len(base.Fragments) / 2
	f := base.Fragments[idx]
	if len(f.Samples) == 0 {
		return Injection{anchor: anchorNone, Detail: "no samples"}, base, nil
	}
	plans, err := base.Plan()
	if err != nil {
		return Injection{}, nil, err
	}
	last := len(plans[idx].Samples) - 1
	forged := make([]byte, 4)
	binary.BigEndian.PutUint32(forged, 32)
	copy(forged, "moof")
	plans[idx].Samples[last].Payload = append(plans[idx].Samples[last].Payload, forged...)
	next, err := base.Reassemble(plans)
	if err != nil {
		return Injection{}, nil, err
	}
	nf := next.Fragments[idx]
	sample := nf.Samples[len(nf.Samples)-1]
	data := append([]byte(nil), next.Data...)
	entrySize := nf.TrunOffset + 8 + 4 + 4 + 4 + int64(8*(len(nf.Samples)-1)) + 4
	overshoot := int64(nf.MdatOffset+nf.MdatSize) - sample.Offset + 8
	declared := sample.Size + int(overshoot)
	binary.BigEndian.PutUint32(data[entrySize:], uint32(declared))
	trap := cloneStream(next, data)
	return Injection{
		Fragment: idx,
		anchor:   anchorTrunSizeField,
		Detail:   fmt.Sprintf("sample size field inflated to %d; forged moof header at %d", declared, sample.Offset+int64(sample.Size)),
	}, trap, nil
}

// injectRTPSequenceGap advances the RTP sequence and timestamp of one sample so
// the RTP-level continuity check reports a gap that the container-level mfhd
// numbers and tfdt do not show. This keeps two independent continuity models
// on the shared error path.
func injectRTPSequenceGap(base *Stream, plans []TrackPlan) (Injection, *Stream, error) {
	if len(plans) == 0 || len(plans[0].Samples) == 0 {
		return Injection{anchor: anchorNone, Detail: "no samples"}, base, nil
	}
	idx := len(plans) / 2
	first := len(plans[idx].Samples) - 1
	// Advance the sequence of this sample and every later sample on the same
	// track. Continuity resumes after the gap, so exactly one loss is
	// reported rather than a gap followed by a phantom reordering.
	for i := idx; i < len(plans); i++ {
		if plans[i].Track.ID != plans[idx].Track.ID {
			continue
		}
		start := 0
		if i == idx {
			start = first
		}
		for k := start; k < len(plans[i].Samples); k++ {
			smp := &plans[i].Samples[k]
			var h rtp.Header
			if n, err := h.Unmarshal(smp.Payload); err != nil || n < 12 {
				return Injection{anchor: anchorNone, Detail: "sample has no RTP header"}, base, nil
			}
			h.SequenceNumber += 3
			h.Timestamp += uint32(smp.Duration) * 3
			if _, err := h.MarshalTo(smp.Payload); err != nil {
				return Injection{}, nil, err
			}
		}
	}
	var last = first
	next, err := base.Reassemble(plans)
	if err != nil {
		return Injection{}, nil, err
	}
	return Injection{
		Fragment: idx,
		anchor:   anchorSamplePayload,
		Detail:   fmt.Sprintf("rtp sequence advanced by 3 at sample %d (sequence field at payload+2)", last),
	}, next, nil
}

func cloneStream(base *Stream, data []byte) *Stream {
	c := *base
	c.Data = data
	c.Digest = digest(data)
	return &c
}
