package container

import (
	"bytes"
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
)

// Check parses data back and verifies it against the track specs:
// brand, track count, per-track timescale, sample entry codec, stts
// delta, stsz sample count, stss keyframe bounds and stco range.
func Check(data []byte, specs []TrackSpec) (*Report, error) {
	r := bytes.NewReader(data)
	boxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeFtyp()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAvc1()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeHvc1()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAv01()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStts()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStco()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStss()},
	})
	if err != nil {
		return nil, fault.New(fault.ErrIntegrity, fault.NoOffset, "container parse: %v", err)
	}
	rep := &Report{}
	var tkhds []*mp4.Tkhd
	var mdhds []*mp4.Mdhd
	var entries []codec.Kind
	var stts []*mp4.Stts
	var stsz []*mp4.Stsz
	var stco []*mp4.Stco
	var stss []*mp4.Stss
	for _, b := range boxes {
		switch p := b.Payload.(type) {
		case *mp4.Ftyp:
			rep.MajorBrand = string(p.MajorBrand[:])
		case *mp4.Tkhd:
			tkhds = append(tkhds, p)
		case *mp4.Mdhd:
			mdhds = append(mdhds, p)
		case *mp4.VisualSampleEntry:
			k, _ := kindOfEntry(b.Info.Type)
			entries = append(entries, k)
		case *mp4.Stts:
			stts = append(stts, p)
		case *mp4.Stsz:
			stsz = append(stsz, p)
		case *mp4.Stco:
			stco = append(stco, p)
		case *mp4.Stss:
			stss = append(stss, p)
		}
	}
	if rep.MajorBrand == "" || len(mdhds) != len(specs) || len(tkhds) != len(specs) {
		return nil, fault.New(fault.ErrMissingTrack, fault.NoOffset,
			"container has %d tracks, want %d", len(mdhds), len(specs))
	}
	for i, spec := range specs {
		tr, err := checkTrack(i, spec, tkhds[i], mdhds[i], entries, stts, stsz, stco, stss, uint64(len(data)))
		if err != nil {
			return nil, err
		}
		rep.Tracks = append(rep.Tracks, *tr)
	}
	return rep, nil
}

func checkTrack(i int, spec TrackSpec, tkhd *mp4.Tkhd, mdhd *mp4.Mdhd,
	entries []codec.Kind, stts []*mp4.Stts, stsz []*mp4.Stsz, stco []*mp4.Stco,
	stss []*mp4.Stss, fileSize uint64) (*TrackReport, error) {
	fail := func(format string, args ...any) error {
		return fault.New(fault.ErrIntegrity, int64(i),
			"track %d: %s", spec.ID, fmt.Sprintf(format, args...))
	}
	if tkhd.TrackID != spec.ID {
		return nil, fail("tkhd id %d", tkhd.TrackID)
	}
	if mdhd.Timescale != spec.Timescale {
		return nil, fail("timescale %d want %d", mdhd.Timescale, spec.Timescale)
	}
	if i >= len(entries) || entries[i] != spec.Kind {
		return nil, fail("sample entry codec mismatch")
	}
	if i >= len(stts) || len(stts[i].Entries) == 0 || stts[i].Entries[0].SampleDelta != spec.SampleDelta {
		return nil, fail("stts delta mismatch")
	}
	if i >= len(stsz) || stsz[i].SampleCount != uint32(len(spec.Samples)) {
		return nil, fail("stsz sample count mismatch")
	}
	if i >= len(stco) || len(stco[i].ChunkOffset) != 1 ||
		uint64(stco[i].ChunkOffset[0]) >= fileSize {
		return nil, fail("stco chunk offset out of range")
	}
	if i < len(stss) {
		for _, kn := range stss[i].SampleNumber {
			if kn == 0 || kn > uint32(len(spec.Samples)) {
				return nil, fail("stss sample %d out of range", kn)
			}
		}
	}
	return &TrackReport{ID: spec.ID, Kind: spec.Kind, Timescale: spec.Timescale,
		SampleCount: uint32(len(spec.Samples)), KeyCount: uint32(len(spec.KeySamples))}, nil
}
