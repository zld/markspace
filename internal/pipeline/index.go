package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"liveregress/internal/fault"
	"liveregress/internal/fixture"
	"liveregress/internal/index"
	"liveregress/internal/manifest"
	"liveregress/internal/verify"
)

// loadIFrameIndex reads the layer's I-frames-only playlist. Entry times come
// from that playlist's own exact declared timeline, which must line up with the
// media playlist's segment starts for the entries to hit real keyframes.
func (r *Runner) loadIFrameIndex(lr *LayerReport, pl *manifest.Playlist, timescale uint32) (*index.Index, error) {
	rel := filepath.ToSlash(filepath.Join(filepath.Dir(pl.Path), fixture.IFrameName))
	if _, err := os.Stat(filepath.Join(r.opts.Root, rel)); err != nil {
		return nil, nil // absent index: caller reports a missing-index fault
	}
	ifp, err := manifest.LoadMedia(r.opts.Root, rel)
	if err != nil {
		return nil, err
	}
	if !ifp.IFramesOnly {
		return nil, fault.New(fault.KindIndex, rel, -1,
			"I-frame playlist is not marked EXT-X-I-FRAMES-ONLY").Recover(false)
	}
	known := map[string]bool{}
	for _, s := range pl.Segments {
		known[filepath.Base(s.Path)] = true
	}
	committed := map[string]bool{}
	for _, s := range lr.Segments {
		if s.Complete {
			committed[filepath.Base(s.Path)] = true
		}
	}
	idx := &index.Index{Layer: lr.Layer, Path: rel, Timescale: timescale, Source: "iframes"}
	for _, s := range ifp.Segments {
		if s.ByteRange == nil {
			return nil, fault.New(fault.KindIndex, rel, -1,
				"I-frame segment has no EXT-X-BYTERANGE").Recover(false)
		}
		if !known[filepath.Base(s.Path)] {
			return nil, fault.New(fault.KindIndex, rel, s.ByteRange.Offset,
				fmt.Sprintf("I-frame playlist references unknown segment %q", s.Path)).Recover(false)
		}
		if !committed[filepath.Base(s.Path)] {
			// A truncated segment cannot be checked against its declared
			// tail; it is reported as truncation, not an index mismatch.
			continue
		}
		idx.Entries = append(idx.Entries, index.Entry{
			Time:      s.Start.Ticks(timescale),
			Timescale: timescale,
			Offset:    s.ByteRange.Offset,
			Size:      uint32(s.ByteRange.Length),
			TrackID:   1,
		})
	}
	if len(idx.Entries) == 0 {
		return nil, fault.New(fault.KindIndex, rel, -1, "I-frame playlist has no entries").Recover(false)
	}
	return idx, nil
}

// rebuildIndex scans every sample's bytes for a sync marker, writes a rebuilt
// index, and verifies it against the keyframes the container flagged. The scan
// is authoritative; a mismatch between scan and flags fails.
func (r *Runner) rebuildIndex(lr *LayerReport, pl *manifest.Playlist, perSegment [][]sampleFact, observed []index.Keyframe, timescale uint32) (*index.Index, error) {
	type source struct {
		path string
		data []byte
		refs map[int64]sampleFact
	}
	var sources []source
	var all []index.SampleRef
	for i, seg := range pl.Segments {
		data, err := os.ReadFile(filepath.Join(r.opts.Root, filepath.FromSlash(seg.Path)))
		if err != nil {
			return nil, fault.Wrap(fault.KindPath, seg.Path, -1, "read segment for rebuild", err)
		}
		refs := make(map[int64]sampleFact, len(perSegment[i]))
		for _, f := range perSegment[i] {
			refs[f.Offset] = f
			all = append(all, index.SampleRef{Time: f.Time, Offset: f.Offset,
				Size: f.Size, TrackID: f.TrackID, IsKeyHint: f.Keyframe})
		}
		sources = append(sources, source{path: seg.Path, data: data, refs: refs})
	}
	if len(all) == 0 {
		return nil, fault.New(fault.KindIndex, pl.Path, -1,
			"no samples available to rebuild an index").Recover(false)
	}
	read := func(ref index.SampleRef) ([]byte, error) {
		for _, s := range sources {
			if _, ok := s.refs[ref.Offset]; !ok {
				continue
			}
			end := ref.Offset + int64(ref.Size)
			if end > int64(len(s.data)) {
				return nil, fault.New(fault.KindTruncate, s.path, ref.Offset,
					"sample extends past segment").Recover(false)
			}
			return s.data[ref.Offset:end], nil
		}
		return nil, fault.New(fault.KindIndex, pl.Path, ref.Offset,
			"scanned sample not present in any segment").Recover(false)
	}
	hasher := verify.NewHasher()
	for _, src := range sources {
		hasher.Write(src.data)
	}
	if err := os.MkdirAll(filepath.Join(r.outRoot, lr.Layer), 0o755); err != nil {
		return nil, fault.Wrap(fault.KindPath, r.outRoot, -1, "create layer output", err)
	}
	idx, err := index.Rebuild(lr.Layer, timescale, index.CodecH264, all, read)
	if err != nil {
		return nil, err
	}
	idx.InputHash = hasher.Sum()
	idx.Path = filepath.ToSlash(filepath.Join(filepath.Dir(pl.Path), fixture.IFrameName))
	if err := idx.Save(filepath.Join(r.outRoot, lr.Layer, "iframes.idx.json")); err != nil {
		return nil, err
	}
	hits, fp, missed := index.Verify(idx, observed)
	if fp > 0 || missed > 0 {
		return nil, fault.New(fault.KindIndex, idx.Path, 0,
			fmt.Sprintf("rebuilt index disagrees with media: hits=%d false=%d missed=%d", hits, fp, missed)).
			Recover(false)
	}
	return idx, nil
}
