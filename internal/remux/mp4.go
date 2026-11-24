// Package remux rewrites MP4 container structures while preserving compressed
// sample payloads byte-for-byte. It never decodes or re-encodes media.
package remux

import (
	"bytes"
	"os"
	"path/filepath"

	gomp4 "github.com/abema/go-mp4"

	"mediapack/internal/demux"
	"mediapack/internal/fault"
)

// WriteMP4 writes tracks in the supplied mapping order. Before writing each
// sample it verifies its payload hash; one mismatch fails the whole operation.
func WriteMP4(path string, timescale uint32, tracks []Track) (*Result, error) {
	if timescale == 0 {
		timescale = 1_000_000
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fault.Wrap(fault.KindPath, err)
	}
	res := &Result{Path: path, Timescale: timescale}
	for i := range tracks {
		if tracks[i].Timescale == 0 {
			tracks[i].Timescale = timescale
		}
	}
	var mdatPayload bytes.Buffer
	type placed struct {
		offset   uint32
		sizes    []uint32
		deltas   []uint32
		comp     []uint32
		duration uint32
	}
	placedTracks := make([]placed, 0, len(tracks))
	mdatStart := uint32(len(ftypBox()) + 8)
	var maxDuration uint32

	for _, tr := range tracks {
		pt := placed{offset: mdatStart + uint32(mdatPayload.Len())}
		for i, s := range tr.Samples {
			gotHash := demux.HashPayload(s.Payload)
			if s.Hash != "" && gotHash != s.Hash {
				res.HashMismatches++
				return res, fault.At(fault.KindHash, path, int64(i),
					"track %d sample %d hash mismatch: manifest=%s actual=%s", tr.ID, i, s.Hash, gotHash)
			}
			if i > 0 && s.DTS < tr.Samples[i-1].DTS {
				return res, fault.At(fault.KindTimeline, path, int64(i),
					"track %d backward DTS", tr.ID)
			}
			d := s.Duration
			if i+1 < len(tr.Samples) && tr.Samples[i+1].DTS > s.DTS {
				d = tr.Samples[i+1].DTS - s.DTS
			}
			if d < 0 || d > int64(^uint32(0)) {
				return res, fault.New(fault.KindTimeline, "track %d sample %d bad duration %d", tr.ID, i, d)
			}
			pt.deltas = append(pt.deltas, uint32(d))
			if s.PTS != s.DTS && s.PTS >= s.DTS {
				pt.comp = append(pt.comp, uint32(s.PTS-s.DTS))
			}
			pt.sizes = append(pt.sizes, uint32(len(s.Payload)))
			mdatPayload.Write(s.Payload)
			res.SampleCount++
		}
		var dur int64
		if n := len(tr.Samples); n > 0 {
			last := tr.Samples[n-1]
			dur = last.DTS
			if len(pt.deltas) > 0 {
				// Sum deltas gives the output decode duration even with source gaps.
				dur = 0
				for _, d := range pt.deltas {
					dur += int64(d)
				}
			} else {
				dur += last.Duration
			}
		}
		if dur < 0 || dur > int64(^uint32(0)) {
			return nil, fault.New(fault.KindTimeline, "track %d duration out of uint32 range", tr.ID)
		}
		pt.duration = uint32(dur)
		if pt.duration > maxDuration {
			maxDuration = pt.duration
		}
		placedTracks = append(placedTracks, pt)
	}

	var moov bytes.Buffer
	moov.Write(mvhdBox(timescale, maxDuration))
	for i, tr := range tracks {
		pt := placedTracks[i]
		moov.Write(trakBox(tr, pt.duration, pt.sizes, pt.deltas, pt.comp, pt.offset))
	}
	output := bytes.NewBuffer(nil)
	output.Write(ftypBox())
	output.Write(boxBytes("mdat", mdatPayload.Bytes()))
	output.Write(boxBytes("moov", moov.Bytes()))

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, output.Bytes(), 0o644); err != nil {
		return nil, fault.Wrap(fault.KindUnrecoverable, err)
	}
	// Parse the freshly rewritten container with go-mp4 before atomic commit.
	if _, err := gomp4.Probe(bytes.NewReader(output.Bytes())); err != nil {
		_ = os.Remove(tmp)
		return nil, fault.Wrap(fault.KindProbe, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fault.Wrap(fault.KindUnrecoverable, err)
	}
	st, _ := os.Stat(path)
	if st != nil {
		res.OutputBytes = st.Size()
	}
	return res, nil
}
