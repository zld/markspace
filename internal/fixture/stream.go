package fixture

import (
	"fmt"
	"math/rand"
)

// MakePlans builds the baseline fragment plan for a spec.
func MakePlans(spec Spec) ([]TrackPlan, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	plans := make([]TrackPlan, 0, spec.Fragments)
	cursors := make([]int64, len(spec.Tracks))
	for i, t := range spec.Tracks {
		cursors[i] = t.SourceStart
	}
	seqs := make([]uint32, len(spec.Tracks))
	rng := rand.New(rand.NewSource(spec.Seed))
	for i := 0; i < spec.Fragments; i++ {
		ti := i % len(spec.Tracks)
		t := spec.Tracks[ti]
		seqs[ti]++
		p := TrackPlan{Track: t, Seq: seqs[ti], Tfdt: cursors[ti]}
		for s := 0; s < spec.SamplesPerFragment; s++ {
			buf := make([]byte, 48+rng.Intn(180))
			rng.Read(buf)
			p.Samples = append(p.Samples, PlanSample{
				Start:    cursors[ti] + int64(s)*int64(t.SampleDuration),
				Duration: int64(t.SampleDuration),
				Payload:  buf,
			})
		}
		cursors[ti] += int64(spec.SamplesPerFragment) * int64(t.SampleDuration)
		plans = append(plans, p)
	}
	return plans, nil
}

// Assemble serializes plans into a complete stream with init, sidx and sample
// fragments. Only the first sidxRefs fragments are referenced by the index,
// which models a partially written index.
func Assemble(spec Spec, plans []TrackPlan, sidxRefs int) (*Stream, error) {
	initBytes, err := BuildInit(spec)
	if err != nil {
		return nil, err
	}
	if sidxRefs < 0 || sidxRefs > len(plans) {
		sidxRefs = len(plans)
	}
	moofs := make([][]byte, len(plans))
	mdats := make([][]byte, len(plans))
	layouts := make([]fragLayout, len(plans))
	for i, p := range plans {
		// The trun data offset does not change the moof size, so a
		// provisional pass at offset zero is enough to learn it.
		probe, _, _, err := buildFragment(p, 0)
		if err != nil {
			return nil, err
		}
		moof, mdat, layout, err := buildFragment(p, int32(len(probe)+8))
		if err != nil {
			return nil, err
		}
		moofs[i], mdats[i], layouts[i] = moof, mdat, layout
	}
	refs := make([]int64, sidxRefs)
	durations := make([]uint32, sidxRefs)
	for i := 0; i < sidxRefs; i++ {
		refs[i] = int64(len(moofs[i]) + len(mdats[i]))
		durations[i] = uint32(layouts[i].fragmentDuration)
	}
	sidx, err := buildSidx(spec.TimelineTimescale, refs, durations)
	if err != nil {
		return nil, err
	}
	s := &Stream{
		Spec:       spec,
		InitSize:   int64(len(initBytes)),
		SidxOffset: int64(len(initBytes)),
		SidxSize:   int64(len(sidx)),
		SidxRefs:   sidxRefs,
	}
	data := append([]byte(nil), initBytes...)
	data = append(data, sidx...)
	for i, p := range plans {
		fi := FragmentInfo{
			Index:      i,
			TrackID:    layouts[i].trackID,
			Sequence:   layouts[i].sequence,
			MoofOffset: int64(len(data)),
			MoofSize:   int64(len(moofs[i])),
			MdatOffset: int64(len(data) + len(moofs[i])),
			MdatSize:   int64(len(mdats[i])),
			TfdtOffset: int64(len(data)) + layouts[i].tfdtOffset,
			Tfdt:       p.Tfdt,
			TrunOffset: int64(len(data)) + layouts[i].trunOffset,
		}
		payloadStart := fi.MdatOffset + 8
		for k, smp := range p.Samples {
			fi.Samples = append(fi.Samples, SampleInfo{
				Index:      k,
				Offset:     payloadStart + layouts[i].sampleOffsets[k],
				Size:       len(smp.Payload),
				PayloadSHA: digest(smp.Payload),
			})
		}
		data = append(data, moofs[i]...)
		data = append(data, mdats[i]...)
		s.Fragments = append(s.Fragments, fi)
	}
	s.Data = data
	s.Digest = digest(data)
	return s, nil
}

// Build serializes the baseline stream for a spec.
func Build(spec Spec) (*Stream, error) {
	plans, err := MakePlans(spec)
	if err != nil {
		return nil, err
	}
	return Assemble(spec, plans, len(plans))
}

// Reassemble rebuilds a stream from mutated plans, preserving the index
// coverage recorded on the original stream.
func (s *Stream) Reassemble(plans []TrackPlan) (*Stream, error) {
	out, err := Assemble(s.Spec, plans, s.SidxRefs)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Plan rebuilds the logical plan from the stream byte map. Payloads are
// copied out of the serialized data so injection cannot alias the source.
func (s *Stream) Plan() ([]TrackPlan, error) {
	plans := make([]TrackPlan, 0, len(s.Fragments))
	byIndex := make(map[int]FragmentInfo, len(s.Fragments))
	for _, f := range s.Fragments {
		byIndex[f.Index] = f
	}
	for i := 0; i < len(s.Fragments); i++ {
		f, ok := byIndex[i]
		if !ok {
			return nil, fmt.Errorf("fixture: fragment %d missing from byte map", i)
		}
		track, ok := s.trackByID(f.TrackID)
		if !ok {
			return nil, fmt.Errorf("fixture: track %d missing from spec", f.TrackID)
		}
		p := TrackPlan{Track: track, Seq: f.Sequence, Tfdt: f.Tfdt}
		for _, smp := range f.Samples {
			raw := append([]byte(nil), s.Data[smp.Offset:smp.Offset+int64(smp.Size)]...)
			p.Samples = append(p.Samples, PlanSample{
				Start:    sourceStart(s, f.TrackID, i, smp.Index),
				Duration: sampleDuration(track),
				Payload:  raw,
			})
		}
		plans = append(plans, p)
	}
	return plans, nil
}

func (s *Stream) trackByID(id uint32) (TrackSpec, bool) {
	for _, t := range s.Spec.Tracks {
		if t.ID == id {
			return t, true
		}
	}
	return TrackSpec{}, false
}

func sampleDuration(t TrackSpec) int64 { return int64(t.SampleDuration) }

// sourceStart reconstructs the declared sample start from the tfdt of the
// fragment and the sample index within it.
func sourceStart(s *Stream, trackID uint32, fragIndex, sampleIndex int) int64 {
	base := s.Fragments[fragIndex].Tfdt
	t, ok := s.trackByID(trackID)
	if !ok {
		return base
	}
	return base + int64(sampleIndex)*int64(t.SampleDuration)
}
