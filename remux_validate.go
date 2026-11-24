package mediacore

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// validateRemux re-opens the output, demuxes it and compares every retained
// sample byte-for-byte and timestamp-for-timestamp with the source.
func validateRemux(src *Container, dst string, res *RemuxResult) error {
	out, err := DemuxFile(dst, DemuxOptions{AllowUnknownTracks: true, ValidateOffsets: true})
	if err != nil {
		return err
	}
	for i := range res.Tracks {
		rt := &res.Tracks[i]
		outTrack := out.TrackByID(rt.TargetID)
		if outTrack == nil {
			return fail(KindContainer, "validate", "output lacks target track %d", rt.TargetID)
		}
		srcTrack := src.TrackByID(rt.SourceID)
		if srcTrack == nil {
			return fail(KindContainer, "validate", "source lacks track %d", rt.SourceID)
		}
		if outTrack.SampleCount() != srcTrack.SampleCount() {
			return fail(KindContainer, "validate", "track %d sample count %d != %d", rt.TargetID, outTrack.SampleCount(), srcTrack.SampleCount())
		}
		inR, err := NewSampleReader(src.Path, srcTrack)
		if err != nil {
			return err
		}
		hash, err := hashTrackSamples(inR)
		inR.Close()
		if err != nil {
			return err
		}
		rt.SourceHash = hash
		outR, err := NewSampleReader(dst, outTrack)
		if err != nil {
			return err
		}
		oh, err := hashTrackSamples(outR)
		outR.Close()
		if err != nil {
			return err
		}
		rt.OutputHash = oh
		if oh != hash {
			return fail(KindContainer, "validate", "track %d payload hash %s != source %s", rt.TargetID, short(oh), short(hash))
		}
		for si := 0; si < srcTrack.SampleCount(); si++ {
			a := srcTrack.Samples[si]
			b := outTrack.Samples[si]
			if a.DTS != b.DTS || a.CTS != b.CTS || a.Duration != b.Duration || a.Sync != b.Sync || a.Size != b.Size {
				return fail(KindTimestamp, "validate", "track %d sample %d metadata differs (dts %d/%d cts %d/%d dur %d/%d sync %t/%t size %d/%d)",
					rt.TargetID, si, a.DTS, b.DTS, a.CTS, b.CTS, a.Duration, b.Duration, a.Sync, b.Sync, a.Size, b.Size)
			}
		}
		if err := compareRawPayloads(src.Path, srcTrack, dst, outTrack); err != nil {
			return err
		}
	}
	return nil
}

// hashTrackSamples hashes payload bytes of every sample in track order.
func hashTrackSamples(r *SampleReader) (string, error) {
	h := sha256.New()
	for i := 0; i < r.track.SampleCount(); i++ {
		if _, err := r.ReadSample(i, h); err != nil {
			return "", err
		}
		fmt.Fprintf(h, "|%d|%d|%d\n", r.track.Samples[i].DTS, r.track.Samples[i].CTS, r.track.Samples[i].Duration)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// sortedSelection normalises the selection map so iteration is deterministic.
func sortedSelection(sel map[uint32][]int) (map[uint32][]int, error) {
	out := make(map[uint32][]int, len(sel))
	keys := make([]uint32, 0, len(sel))
	for k := range sel {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		idx := append([]int(nil), sel[k]...)
		sort.Ints(idx)
		out[k] = idx
	}
	return out, nil
}

func compareRawPayloads(srcPath string, a *Track, outPath string, b *Track) error {
	ra, err := NewSampleReader(srcPath, a)
	if err != nil {
		return err
	}
	defer ra.Close()
	rb, err := NewSampleReader(outPath, b)
	if err != nil {
		return err
	}
	defer rb.Close()
	for i := 0; i < a.SampleCount(); i++ {
		ha, err := ra.SampleHash(i)
		if err != nil {
			return err
		}
		hb, err := rb.SampleHash(i)
		if err != nil {
			return err
		}
		if ha != hb {
			return fail(KindContainer, "validate", "track %d sample %d bytes differ", a.ID, i)
		}
	}
	return nil
}
