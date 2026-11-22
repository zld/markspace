package scan

import (
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediacore/internal/clock"
)

// parseTracks extracts the single supported track and validates every sample
// table. Multi-track assets and unsupported sample descriptions are rejected
// with a classified error naming the offending track.
func parseTracks(r io.ReadSeeker, fileSize uint64, m *Manifest, cfg Config) ([]*TrackScan, error) {
	const op = "scan.trak"
	traks, err := mp4.ExtractBoxes(r, nil, []mp4.BoxPath{{mp4.BoxTypeMoov(), mp4.BoxTypeTrak()}})
	if err != nil {
		return nil, clock.ErrRecoverablef(op, "extract trak: %v", err)
	}
	if len(traks) == 0 {
		return nil, clock.ErrRecoverablef(op, "no trak box present")
	}
	if len(traks) > 1 {
		return nil, clock.ErrRecoverablef(op, "%d tracks present: multi-track assets not supported", len(traks))
	}

	trakBI := traks[0]
	mdhdPayload, err := onePayload(r, trakBI, mp4.BoxTypeMdhd())
	if err != nil {
		return nil, clock.WithTrack(err, m.TrackID, -1)
	}
	mdhd := mdhdPayload.(*mp4.Mdhd)
	if mdhd.Timescale != m.Timescale {
		return nil, clock.WithTrack(clock.ErrRecoverablef(op,
			"mdhd timescale %d != manifest timescale %d", mdhd.Timescale, m.Timescale), m.TrackID, -1)
	}
	basis := clock.TimeBase{TrackID: m.TrackID, Timescale: mdhd.Timescale}
	mediaDur := int64(mdhd.DurationV1)
	if mdhd.GetVersion() == 0 {
		mediaDur = int64(mdhd.DurationV0)
	}

	stbl, err := extractStbl(r, trakBI)
	if err != nil {
		return nil, clock.WithTrack(err, m.TrackID, -1)
	}
	tab, count, err := buildTables(r, trakBI, stbl, fileSize, cfg)
	if err != nil {
		return nil, clock.WithTrack(err, m.TrackID, -1)
	}
	if count != m.SampleCount {
		return nil, clock.WithTrack(clock.ErrRecoverablef(op,
			"stsz sample_count %d != manifest %d", count, m.SampleCount), m.TrackID, -1)
	}

	edits, err := ResolveEdits(r, basis, mediaDur)
	if err != nil {
		return nil, err
	}
	mdatEnd, err := locateMdatEnd(r, fileSize)
	if err != nil {
		return nil, err
	}
	ts := &TrackScan{
		TrackID:  m.TrackID,
		Basis:    basis,
		Edits:    edits,
		MediaDur: mediaDur,
		SyncCount: uint32(len(tab.sync)),
		Count:    count,
		tables:   tab,
		mdatEnd:  mdatEnd,
		manifest: m,
	}
	return []*TrackScan{ts}, nil
}

func extractStbl(r io.ReadSeeker, trak *mp4.BoxInfo) (*mp4.BoxInfo, error) {
	bis, err := mp4.ExtractBoxes(r, trak, []mp4.BoxPath{{
		mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(),
	}})
	if err != nil {
		return nil, clock.ErrRecoverablef("scan.stbl", "extract stbl: %v", err)
	}
	if len(bis) != 1 {
		return nil, clock.ErrRecoverablef("scan.stbl", "expected exactly one stbl, got %d", len(bis))
	}
	return bis[0], nil
}

func onePayload(r io.ReadSeeker, parent *mp4.BoxInfo, t mp4.BoxType) (mp4.IBox, error) {
	got, err := mp4.ExtractBoxWithPayload(r, parent, mp4.BoxPath{t})
	if err != nil {
		return nil, clock.ErrRecoverablef("scan."+string(t[:]), "extract: %v", err)
	}
	if len(got) != 1 {
		return nil, clock.ErrRecoverablef("scan."+string(t[:]), "expected one box, got %d", len(got))
	}
	return got[0].Payload, nil
}

func locateMdatEnd(r io.ReadSeeker, fileSize uint64) (uint64, error) {
	bis, err := mp4.ExtractBoxes(r, nil, []mp4.BoxPath{{mp4.BoxTypeMdat()}})
	if err != nil {
		return 0, clock.ErrRecoverablef("scan.mdat", "extract mdat: %v", err)
	}
	if len(bis) != 1 {
		return 0, clock.ErrRecoverablef("scan.mdat", "expected exactly one mdat, got %d", len(bis))
	}
	end := bis[0].Offset + bis[0].Size
	if end > fileSize {
		return 0, clock.ErrRecoverablef("scan.mdat", "mdat ends at %d beyond file size %d (truncated)", end, fileSize)
	}
	return end, nil
}
