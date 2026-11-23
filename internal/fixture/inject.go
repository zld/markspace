package fixture

import (
	"encoding/binary"
	"fmt"
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
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// Injection records where a fault was applied, in logical and byte terms.
type Injection struct {
	Kind       FaultKind `json:"kind"`
	TrackID    uint32    `json:"track_id,omitempty"`
	Fragment   int       `json:"fragment_index,omitempty"`
	Sequence   uint32    `json:"sequence,omitempty"`
	ByteOffset int64     `json:"byte_offset"`
	Detail     string    `json:"detail,omitempty"`
}

// InjectedStream is the damaged stream plus an exact description of the
// damage. ByteOffset is the raw input offset a parser must report.
type InjectedStream struct {
	*Stream
	Injections []Injection `json:"injections"`
}

// Inject applies the requested faults to a fresh baseline stream. Each fault
// is applied to an independent rebuild of the baseline plans so offsets stay
// deterministic for a fixed seed.
func Inject(seed int64, tracks, fragments int, kinds []FaultKind) (*InjectedStream, error) {
	spec := DefaultSpec(seed, tracks, fragments)
	base, err := Build(spec)
	if err != nil {
		return nil, err
	}
	result := &InjectedStream{Stream: base}
	// Every fault is a pure transform of the baseline plan, applied in the
	// order given. Truncation is always last because it changes the file end.
	var truncateAt, truncateBox int64 = -1, -1
	for _, kind := range kinds {
		if kind == FaultTruncation {
			if truncateAt < 0 {
				truncateAt, truncateBox = truncationPoint(base)
			}
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
		if next == nil {
			// Kind has no effect for this stream shape (e.g. too few
			// fragments); record it with the baseline offset so the count
			// still reflects the request.
			result.Injections = append(result.Injections, inj)
			continue
		}
		result.Stream = next
		result.Injections = append(result.Injections, inj)
	}
	if truncateAt >= 0 {
		if truncateAt > int64(len(result.Data)) {
			truncateAt = int64(len(result.Data))
		}
		inj := Injection{
			Kind:       FaultTruncation,
			ByteOffset: truncateBox,
			Detail:     fmt.Sprintf("file truncated to %d bytes inside box at %d", truncateAt, truncateBox),
		}
		result.Data = result.Data[:truncateAt]
		result.Digest = digest(result.Data)
		result.Injections = append(result.Injections, inj)
	}
	return result, nil
}

// truncationPoint picks a mid-file cut inside a moof body. The moof header
// stays complete and declares more bytes than remain, so a parser must report
// a truncated box anchored at that moof header offset.
func truncationPoint(s *Stream) (cut, boxOffset int64) {
	if len(s.Fragments) == 0 {
		return int64(len(s.Data)), int64(len(s.Data))
	}
	f := s.Fragments[len(s.Fragments)/2]
	return f.MoofOffset + 40, f.MoofOffset
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
	default:
		return Injection{}, nil, fmt.Errorf("fixture: unsupported fault %q", kind)
	}
}

// injectTfdtRegression rewinds the tfdt of the last fragment of each track so
// later fragments appear earlier than their predecessor.
func injectTfdtRegression(base *Stream, plans []TrackPlan) (Injection, *Stream, error) {
	victim := -1
	for i := len(plans) - 1; i >= 0; i-- {
		if i >= len(plans)/2 {
			victim = i
			break
		}
	}
	if victim < 0 || plans[victim].Tfdt == 0 {
		return Injection{Kind: FaultTfdtRegression, Detail: "no stepping tfdt available"}, base, nil
	}
	regressed := plans[victim].Tfdt / 2
	plans[victim].Tfdt = regressed
	next, err := base.Reassemble(plans)
	if err != nil {
		return Injection{}, nil, err
	}
	f := next.Fragments[victim]
	return Injection{
		Kind:       FaultTfdtRegression,
		TrackID:    f.TrackID,
		Fragment:   victim,
		Sequence:   f.Sequence,
		ByteOffset: f.TfdtOffset,
		Detail:     fmt.Sprintf("tfdt regressed to %d", regressed),
	}, next, nil
}

// injectIndexGap rewrites the sidx so it references only the first half of the
// fragments; the remaining moofs still exist in the file.
func injectIndexGap(base *Stream) (Injection, *Stream, error) {
	if len(base.Fragments) < 2 {
		return Injection{Kind: FaultIndexGap, Detail: "too few fragments"}, base, nil
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
	// The first uncovered fragment begins right after the last covered one.
	firstUncovered := next.Fragments[covered].MoofOffset
	return Injection{
		Kind:       FaultIndexGap,
		Fragment:   covered,
		ByteOffset: firstUncovered,
		Detail:     fmt.Sprintf("sidx covers %d of %d fragments", covered, len(next.Fragments)),
	}, next, nil
}

// injectOversizeBox rewrites one mdat size to run past the end of the file.
func injectOversizeBox(base *Stream) (Injection, *Stream, error) {
	if len(base.Fragments) == 0 {
		return Injection{Kind: FaultOversizeBox, Detail: "no fragments"}, base, nil
	}
	idx := len(base.Fragments) / 3
	f := base.Fragments[idx]
	data := append([]byte(nil), base.Data...)
	// Declare a length that runs well past the real end of file.
	declared := int64(len(data)) - f.MdatOffset + 4096
	binary.BigEndian.PutUint32(data[f.MdatOffset:], uint32(declared))
	next := cloneStream(base, data)
	return Injection{
		Kind:       FaultOversizeBox,
		TrackID:    f.TrackID,
		Fragment:   idx,
		Sequence:   f.Sequence,
		ByteOffset: f.MdatOffset,
		Detail:     fmt.Sprintf("mdat declares %d bytes, %d available", declared, int64(len(data))-f.MdatOffset),
	}, next, nil
}

// injectSequenceGap drops one fragment from the middle of the stream. Later
// fragments of the same track keep their mfhd numbers, so a gap appears in the
// sequence and in decode time, exactly as if the capture lost a fragment.
func injectSequenceGap(base *Stream, plans []TrackPlan) (Injection, *Stream, error) {
	if len(plans) < 4 {
		return Injection{Kind: FaultSequenceGap, Detail: "too few fragments"}, base, nil
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
		return Injection{Kind: FaultSequenceGap, Detail: "no mid-track fragment"}, base, nil
	}
	dropped := plans[idx]
	plans = append(plans[:idx], plans[idx+1:]...)
	next, err := base.Reassemble(plans)
	if err != nil {
		return Injection{}, nil, err
	}
	// The next fragment of the same track is where the missing sequence
	// number becomes observable.
	observed := next.Fragments[idx]
	return Injection{
		Kind:       FaultSequenceGap,
		TrackID:    observed.TrackID,
		Fragment:   idx,
		Sequence:   observed.Sequence,
		ByteOffset: observed.MoofOffset,
		Detail:     fmt.Sprintf("dropped fragment sequence %d; next is %d", dropped.Seq, observed.Sequence),
	}, next, nil
}

// injectDuplicate re-emits one fragment immediately after itself, keeping the
// original mfhd sequence and tfdt.
func injectDuplicate(base *Stream, plans []TrackPlan) (Injection, *Stream, error) {
	if len(plans) < 2 {
		return Injection{Kind: FaultDuplicate, Detail: "too few fragments"}, base, nil
	}
	idx := len(plans) / 2
	if idx%len(base.Spec.Tracks) != 0 {
		idx-- // land on the first track so duplicates are unambiguous
	}
	dup := plans[idx]
	dup.Samples = append([]PlanSample(nil), plans[idx].Samples...)
	plans = append(plans[:idx+1], append([]TrackPlan{dup}, plans[idx+1:]...)...)
	next, err := base.Reassemble(plans)
	if err != nil {
		return Injection{}, nil, err
	}
	f := next.Fragments[idx+1]
	return Injection{
		Kind:       FaultDuplicate,
		TrackID:    f.TrackID,
		Fragment:   idx + 1,
		Sequence:   f.Sequence,
		ByteOffset: f.MoofOffset,
		Detail:     fmt.Sprintf("fragment %d repeated", idx),
	}, next, nil
}

// injectCorruptMdat forges a moof header at the start of an mdat payload and
// inflates the trun sample size that covers it. A scanner that resynchronises
// on box magic instead of following declared sizes would report a phantom
// fragment; a size-driven parser reports the sample range violation at the
// forged offset instead.
func injectCorruptMdat(base *Stream) (Injection, *Stream, error) {
	if len(base.Fragments) == 0 {
		return Injection{Kind: FaultCorruptMdat, Detail: "no fragments"}, base, nil
	}
	idx := len(base.Fragments) / 2
	f := base.Fragments[idx]
	if len(f.Samples) == 0 {
		return Injection{Kind: FaultCorruptMdat, Detail: "no samples"}, base, nil
	}
	plans, err := base.Plan()
	if err != nil {
		return Injection{}, nil, err
	}
	last := len(plans[idx].Samples) - 1
	// Point the trun sample size at the last four bytes of the fragment, which
	// hold a forged moof header whose declared size runs past the mdat.
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
	// Inflate the trun sample size so only the last sample runs past the
	// mdat; the sample-size field is the byte actually damaged and therefore
	// the offset a parser must report.
	data := append([]byte(nil), next.Data...)
	entrySize := nf.TrunOffset + 8 + 4 + 4 + 4 + int64(8*(len(nf.Samples)-1)) + 4
	overshoot := int64(nf.MdatOffset+nf.MdatSize) - sample.Offset + 8
	declared := sample.Size + int(overshoot)
	binary.BigEndian.PutUint32(data[entrySize:], uint32(declared))
	trap := cloneStream(next, data)
	return Injection{
		Kind:       FaultCorruptMdat,
		TrackID:    nf.TrackID,
		Fragment:   idx,
		Sequence:   nf.Sequence,
		ByteOffset: entrySize,
		Detail:     fmt.Sprintf("sample size field inflated to %d; forged moof header at %d", declared, sample.Offset+int64(sample.Size)-4),
	}, trap, nil
}

func cloneStream(base *Stream, data []byte) *Stream {
	c := *base
	c.Data = data
	c.Digest = digest(data)
	return &c
}
