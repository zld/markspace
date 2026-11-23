package inspect

import (
	"encoding/binary"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"

	"archivecheck/internal/model"
)

// LoadSamples decodes the sample tables of every moof/mdat pair in the
// fragment. Each sample carries its trun offset so truncation is reported at
// the exact byte.
func (f *Fragment) LoadSamples(init *Init, opts Options) {
	if init == nil {
		f.add(model.NewFault(model.CodeMP4Structure, model.SeverityError,
			src(f.File, 0), "sample tables need an initialization segment"))
		return
	}
	trackIndex := map[uint32]int{}
	for bi := range f.Boxes {
		box := f.Boxes[bi]
		if box.Type != "moof" {
			continue
		}
		var mdat *Box
		if bi+1 < len(f.Boxes) && f.Boxes[bi+1].Type == "mdat" {
			mdat = &f.Boxes[bi+1]
		}
		if mdat == nil && f.Truncated != nil && f.Truncated.Source.Offset == box.Offset+box.Size {
			// The truncated mdat was rejected before it could be recorded as a
			// box. Reconstruct its declared span so samples past the cut still
			// localise to the right byte instead of being misreported as absent.
			if span, ok := declaredTruncatedMdat(f.data, box); ok {
				mdat = &span
			}
		}
		if mdat == nil {
			f.add(model.NewFault(model.CodeMP4Structure, model.SeverityError,
				src(f.File, box.Offset), "moof at %d is not followed by an mdat", box.Offset).
				WithBytes(box.Offset, box.Offset+box.Size))
			continue
		}
		f.decodeMoofSamples(box, *mdat, init, opts, trackIndex)
	}
	sortTracks(f.Tracks)
	detectNonMonotonic(f)
}

// decodeMoofSamples decodes one moof/mdat pair. A damaged trun stops that
// fragment's decode but keeps everything already attributed to the track.
func (f *Fragment) decodeMoofSamples(moofBox, mdatBox Box, init *Init, opts Options, trackIndex map[uint32]int) {
	moof, fault := decodeMoof(f.File, f.data, moofBox)
	if fault != nil {
		f.add(fault)
		return
	}
	for _, traf := range moof.Trafs {
		if traf.Tfhd == nil || traf.Tfdt == nil {
			f.add(model.NewFault(model.CodeMP4Structure, model.SeverityError,
				src(f.File, moofBox.Offset), "traf is missing tfhd or tfdt").
				WithBytes(moofBox.Offset, moofBox.Offset+moofBox.Size))
			continue
		}
		id := traf.Tfhd.TrackID
		meta, ok := init.TrackByID(id)
		if !ok {
			f.add(model.NewFault(model.CodeMP4Structure, model.SeverityError,
				src(f.File, moofBox.Offset), "track %d is not declared by the init segment", id))
			continue
		}
		idx, seen := trackIndex[id]
		if !seen {
			f.Tracks = append(f.Tracks, Track{TrackID: id, Timescale: meta.Timescale, Handler: meta.Handler})
			idx = len(f.Tracks) - 1
			trackIndex[id] = idx
		}
		track := &f.Tracks[idx]
		baseTime := traf.Tfdt.BaseMediaDecodeTime()
		trex := trexFor(init, id)
		for ti, trun := range traf.Truns {
			dataStart, ok := trunDataStart(traf, trun, moofBox, mdatBox)
			if !ok {
				f.add(model.NewFault(model.CodeMP4SampleTable, model.SeverityError,
					src(f.File, moofBox.Offset), "trun %d points outside its mdat", ti).
					WithBytes(moofBox.Offset, moofBox.Offset+moofBox.Size))
				break
			}
			samples, fault := decodeTrun(f.File, trun, dataStart, baseTime, trex, mdatBox, opts, len(f.data))
			if fault != nil {
				f.add(fault)
				break
			}
			for _, s := range samples {
				s.Sequence = len(track.Samples)
				if s.Sync {
					track.SyncSamples = append(track.SyncSamples, s.Sequence)
				}
				track.Samples = append(track.Samples, s)
			}
			for _, s := range samples {
				baseTime += uint64(s.Duration)
			}
		}
	}
}

// declaredTruncatedMdat recovers the declared box header when the mdat's
// declared size exceeds the bytes present.
func declaredTruncatedMdat(data []byte, moof Box) (Box, bool) {
	pos := moof.Offset + moof.Size
	if pos+8 > int64(len(data)) {
		return Box{}, false
	}
	size := int64(binary.BigEndian.Uint32(data[pos:]))
	typ := string(data[pos+4 : pos+8])
	if typ != "mdat" || size < 8 {
		return Box{}, false
	}
	return Box{Type: typ, Offset: pos, Size: size, HdrLen: 8}, true
}

// trunDataStart resolves the first-sample file offset following ISO/IEC
// 14496-12 8.8.7.1.
func trunDataStart(traf *mp4.TrafBox, trun *mp4.TrunBox, moofBox, mdatBox Box) (int64, bool) {
	base := moofBox.Offset
	if traf.Tfhd.HasBaseDataOffset() {
		base = int64(traf.Tfhd.BaseDataOffset)
	}
	if trun.HasDataOffset() {
		base += int64(trun.DataOffset)
	} else {
		base = mdatBox.PayloadOffset()
	}
	if base < mdatBox.PayloadOffset() || base > mdatBox.Offset+mdatBox.Size {
		return 0, false
	}
	return base, true
}

// decodeTrun walks one trun, validating each sample size against the bytes
// actually present in the mdat. This localises sample-table truncation.
func decodeTrun(name string, trun *mp4.TrunBox, dataStart int64, baseTime uint64, trex *mp4.TrexBox, mdat Box, opts Options, availableData int) ([]Sample, *model.Fault) {
	mdatEnd := mdat.Offset + mdat.Size
	off := dataStart
	dec := baseTime
	var out []Sample
	for i, s := range trun.Samples {
		if len(out) >= opts.maxSamples() {
			return out, model.NewFault(model.CodeMP4SampleTable, model.SeverityError,
				src(name, mdat.Offset), "sample count exceeds %d", opts.maxSamples())
		}
		dur, size, flags := s.Dur, s.Size, s.Flags
		if trex != nil {
			if dur == 0 {
				dur = trex.DefaultSampleDuration
			}
			if size == 0 {
				size = trex.DefaultSampleSize
			}
			if flags == 0 {
				flags = trex.DefaultSampleFlags
			}
		}
		// The mdat may declare more bytes than the file holds; both the mdat's
		// declared end and the actually-available bytes bound a sample.
		available := int64(mdat.Size)
		if mdat.Offset+mdat.Size > int64(availableData) {
			available = int64(availableData) - mdat.Offset
		}
		if size > 0 && (off < mdat.PayloadOffset() || off+int64(size) > mdat.PayloadOffset()+available-mdat.HdrLen) {
			return out, model.NewFault(model.CodeMP4SampleTable, model.SeverityError,
				src(name, off),
				"sample %d needs %d bytes but its fragment ends after %d", i, size, mdatEnd-off).
				WithBytes(off, mdatEnd)
		}
		out = append(out, Sample{
			DecodeTime:        dec,
			Duration:          dur,
			Size:              size,
			Flags:             flags,
			CompositionOffset: s.CompositionTimeOffset,
			Sync:              sampleFlagsSync(flags),
			DataOffset:        off,
		})
		off += int64(size)
		dec += uint64(dur)
	}
	return out, nil
}

func sortTracks(tracks []Track) {
	sort.SliceStable(tracks, func(i, j int) bool { return tracks[i].TrackID < tracks[j].TrackID })
}

// detectNonMonotonic flags a decode timeline that steps backwards inside one
// track, pointing at the offending sample's byte offset.
func detectNonMonotonic(f *Fragment) {
	for ti := range f.Tracks {
		tr := &f.Tracks[ti]
		for i := 1; i < len(tr.Samples); i++ {
			cur, prev := tr.Samples[i], tr.Samples[i-1]
			if cur.DecodeTime < prev.DecodeTime {
				f.add(model.NewFault(model.CodeMP4NonMonotonic, model.SeverityError,
					model.Source{Layer: "inspect", File: f.File, Offset: cur.DataOffset},
					"track %d sample %d decodes at %d, before sample %d at %d",
					tr.TrackID, i, cur.DecodeTime, i-1, prev.DecodeTime).
					WithBytes(cur.DataOffset, cur.DataOffset+int64(cur.Size)))
			}
		}
	}
}
