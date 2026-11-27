package index

import (
	"github.com/abema/go-mp4"

	"mediasync/internal/model"
)

// normalize walks the parsed tree and fills ScanResult with movie metadata
// and per-track nanosecond sample boundaries.
func normalize(res *ScanResult, roots []*boxNode, limits model.Limits) error {
	moov := findType(roots, mp4.BoxTypeMoov())
	if moov == nil {
		return model.AtError(model.ClassCorruptBox, res.InputPath, 0, 0, "no moov box found")
	}
	if mvhdNode := findType(moov.children, mp4.BoxTypeMvhd()); mvhdNode != nil {
		if mvhd := payloadAs[*mp4.Mvhd](mvhdNode); mvhd != nil {
			res.Timescale = mvhd.Timescale
			dur := uint64(mvhd.DurationV1)
			if mvhd.GetVersion() == 0 {
				dur = uint64(mvhd.DurationV0)
			}
			if mvhd.Timescale != 0 {
				if t, err := model.DurationTicks(dur, uint64(mvhd.Timescale)); err == nil {
					res.EndNS = t
				}
			}
		}
	}
	for _, trak := range findAll(moov.children, mp4.BoxTypeTrak()) {
		tr, err := normalizeTrack(res.InputPath, trak, limits)
		if err != nil {
			// One bad track must not erase the other valid tracks.
			if me := model.AsMediaError(err); me != nil {
				res.Anomalies = append(res.Anomalies, *me)
				continue
			}
			return err
		}
		res.Tracks = append(res.Tracks, tr)
		if len(tr.Samples) > 0 && (res.StartNS == 0 || tr.StartNS < res.StartNS) {
			res.StartNS = tr.StartNS
		}
		if tr.EndNS > res.EndNS {
			res.EndNS = tr.EndNS
		}
	}
	return nil
}

func normalizeTrack(path string, trak *boxNode, limits model.Limits) (TrackIndex, error) {
	mdia := findType(trak.children, mp4.BoxTypeMdia())
	if mdia == nil {
		return TrackIndex{}, model.AtError(model.ClassCorruptBox, path, 0, int64(trak.info.Offset), "trak missing mdia")
	}
	mdhdNode := findType(mdia.children, mp4.BoxTypeMdhd())
	mdhd := payloadAs[*mp4.Mdhd](mdhdNode)
	if mdhd == nil || mdhd.Timescale == 0 {
		return TrackIndex{}, model.AtError(model.ClassInvalidTimebase, path, 0,
			offsetOf(mdhdNode), "mdhd missing or zero timescale")
	}
	tr := TrackIndex{Timescale: mdhd.Timescale}
	if tkhd := payloadAs[*mp4.Tkhd](findType(trak.children, mp4.BoxTypeTkhd())); tkhd != nil {
		tr.TrackID = tkhd.TrackID
	}
	if hdlr := payloadAs[*mp4.Hdlr](findType(mdia.children, mp4.BoxTypeHdlr())); hdlr != nil {
		tr.Handler = string(hdlr.HandlerType[:])
	}
	minf := findType(mdia.children, mp4.BoxTypeMinf())
	if minf == nil {
		return tr, nil // metadata-only track: no samples to index
	}
	stbl := findType(minf.children, mp4.BoxTypeStbl())
	if stbl == nil {
		return tr, model.AtError(model.ClassCorruptBox, path, tr.TrackID,
			int64(minf.info.Offset), "minf missing stbl")
	}
	samples, err := buildSamples(path, tr, stbl, limits)
	if err != nil {
		return tr, err
	}
	tr.Samples = samples
	tr.SampleCount = len(samples)
	if len(samples) > 0 {
		tr.StartNS = samples[0].StartNS
		tr.EndNS = samples[len(samples)-1].EndNS
	}
	return tr, nil
}

// buildSamples expands stts/stsc/stsz/stco into one boundary per sample and
// converts every timestamp to the nanosecond base.
func buildSamples(path string, tr TrackIndex, stbl *boxNode, limits model.Limits) ([]SampleBoundary, error) {
	sttsNode := findType(stbl.children, mp4.BoxTypeStts())
	stscNode := findType(stbl.children, mp4.BoxTypeStsc())
	stszNode := findType(stbl.children, mp4.BoxTypeStsz())
	stcoNode := findType(stbl.children, mp4.BoxTypeStco())
	co64Node := findType(stbl.children, mp4.BoxTypeCo64())
	stts := payloadAs[*mp4.Stts](sttsNode)
	stsc := payloadAs[*mp4.Stsc](stscNode)
	stsz := payloadAs[*mp4.Stsz](stszNode)
	stco := payloadAs[*mp4.Stco](stcoNode)
	co64 := payloadAs[*mp4.Co64](co64Node)
	if stts == nil || stsc == nil || stsz == nil || (stco == nil && co64 == nil) {
		return nil, model.AtError(model.ClassCorruptBox, path, tr.TrackID,
			int64(stbl.info.Offset), "stbl missing one of stts/stsc/stsz/stco")
	}
	deltas, err := expandStts(path, tr.TrackID, sttsNode, stts, limits)
	if err != nil {
		return nil, err
	}
	sizes := expandStsz(stsz, len(deltas))
	if len(sizes) != len(deltas) {
		return nil, model.AtError(model.ClassCorruptBox, path, tr.TrackID,
			int64(stszNode.info.Offset), "stsz count %d != stts count %d", len(sizes), len(deltas))
	}
	offsets, err := expandChunks(path, tr, stscNode, stsc, stco, co64, sizes, stbl)
	if err != nil {
		return nil, err
	}
	out := make([]SampleBoundary, len(deltas))
	var cur model.Ticks
	var prev model.Ticks
	for i, d := range deltas {
		start, err := model.ToTicks(int64(cur), tr.Timescale)
		if err != nil {
			return nil, annotate(err, path, tr.TrackID, int64(sttsNode.info.Offset))
		}
		dur, err := model.ToTicks(int64(d), tr.Timescale)
		if err != nil {
			return nil, annotate(err, path, tr.TrackID, int64(sttsNode.info.Offset))
		}
		if i > 0 && start < prev {
			return nil, model.AtError(model.ClassTimeRewind, path, tr.TrackID,
				int64(sttsNode.info.Offset), "sample %d starts %d before previous", i, start)
		}
		out[i] = SampleBoundary{Index: i, Offset: offsets[i], Size: int64(sizes[i]), StartNS: start, EndNS: start + dur}
		prev, cur = start, cur+model.Ticks(d)
	}
	return out, nil
}

func expandStts(path string, trackID uint32, node *boxNode, stts *mp4.Stts, limits model.Limits) ([]uint32, error) {
	var total uint64
	for _, e := range stts.Entries {
		total += uint64(e.SampleCount)
	}
	if total > uint64(limits.MaxTrackSamples) {
		return nil, model.AtError(model.ClassLimitExceeded, path, trackID,
			int64(node.info.Offset), "sample count %d exceeds limit %d", total, limits.MaxTrackSamples)
	}
	out := make([]uint32, 0, total)
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			out = append(out, e.SampleDelta)
		}
	}
	return out, nil
}

func expandStsz(stsz *mp4.Stsz, n int) []uint32 {
	if stsz.SampleSize != 0 {
		out := make([]uint32, n)
		for i := range out {
			out[i] = stsz.SampleSize
		}
		return out
	}
	return stsz.EntrySize
}
