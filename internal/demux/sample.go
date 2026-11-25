// Package demux reads MP4/MOV containers and RTP dumps into the unified
// event model. Unsupported structures and truncated data are reported
// with track, raw byte offset and error category; nothing is dropped
// silently.
package demux

import (
	"encoding/binary"

	"mediacore/internal/clock"
	"mediacore/internal/model"
)

// buildSampleTable expands stts/ctts/stsz/stsc/stco into per-sample records.
func (d *mp4Demuxer) buildSampleTable(tb *trackBuilder, trakOffset uint64) ([]sample, error) {
	id := d.trackID(tb)
	if tb.stts == nil || tb.stsz == nil || tb.stsc == nil {
		return nil, model.NewError(model.ErrUnsupported, model.StageDemux, id, int64(trakOffset),
			"track missing stts/stsz/stsc")
	}
	n := int(tb.stsz.SampleCount)
	if n == 0 {
		return nil, nil // zero-sample track is valid
	}
	if len(tb.chunks) == 0 {
		return nil, model.NewError(model.ErrUnsupported, model.StageDemux, id, int64(trakOffset),
			"track has samples but no chunk offsets")
	}
	if n > d.opts.MaxSamples {
		return nil, model.NewError(model.ErrBadLength, model.StageDemux, id, int64(trakOffset),
			"sample count %d exceeds limit %d", n, d.opts.MaxSamples)
	}
	sizes := tb.stsz.EntrySize
	if tb.stsz.SampleSize != 0 {
		sizes = make([]uint32, n)
		for i := range sizes {
			sizes[i] = tb.stsz.SampleSize
		}
	}
	if len(sizes) < n {
		return nil, model.NewError(model.ErrBadLength, model.StageDemux, id, int64(trakOffset),
			"stsz entries %d < sample count %d", len(sizes), n)
	}
	samples := make([]sample, 0, n)
	var dts int64
	idx := 0
	for _, e := range tb.stts.Entries {
		for k := uint32(0); k < e.SampleCount && idx < n; k++ {
			pts := dts + tb.mediaTime
			if tb.ctts != nil {
				pts = dts + tb.mediaTime + tb.ctts.GetSampleOffset(idx)
			}
			samples = append(samples, sample{size: sizes[idx], pts: pts, dur: e.SampleDelta})
			dts += int64(e.SampleDelta)
			idx++
		}
	}
	if len(samples) < n {
		return nil, model.NewError(model.ErrBadLength, model.StageDemux, id, int64(trakOffset),
			"stts covers %d of %d samples", len(samples), n)
	}
	// Assign chunk offsets via stsc.
	si := 0
	for ci := 0; ci < len(tb.chunks) && si < n; ci++ {
		chunk1 := uint32(ci + 1)
		entry := tb.stsc.Entries[0]
		for _, e := range tb.stsc.Entries {
			if e.FirstChunk <= chunk1 {
				entry = e
			}
		}
		off := tb.chunks[ci]
		for k := uint32(0); k < entry.SamplesPerChunk && si < n; k++ {
			if uint64(sizes[si]) > d.opts.MaxSampleSize {
				return nil, model.NewError(model.ErrBadLength, model.StageDemux, id, int64(off),
					"sample size %d exceeds limit", sizes[si])
			}
			if off+uint64(sizes[si]) > uint64(d.size) {
				return nil, model.NewError(model.ErrTruncated, model.StageDemux, id, int64(off),
					"sample data beyond file size %d", d.size)
			}
			samples[si].offset = off
			off += uint64(sizes[si])
			si++
		}
	}
	if si < n {
		return nil, model.NewError(model.ErrBadLength, model.StageDemux, id, int64(trakOffset),
			"chunks cover %d of %d samples", si, n)
	}
	return samples, nil
}

func (d *mp4Demuxer) finalizeTrack(tb *trackBuilder, trakOffset uint64) error {
	id := d.trackID(tb)
	if tb.timescale == 0 {
		return model.NewError(model.ErrUnsupported, model.StageDemux, id, int64(trakOffset), "missing mdhd timescale")
	}
	kind, ok := trackKind(tb.handler, tb.entryType)
	if !ok {
		return model.NewError(model.ErrUnsupported, model.StageDemux, id, int64(trakOffset),
			"handler %q not supported", tb.handler)
	}
	samples, err := d.buildSampleTable(tb, trakOffset)
	if err != nil {
		return err
	}
	d.tracks = append(d.tracks, model.Track{ID: id, Kind: kind, Timescale: tb.timescale, Handler: tb.handler})
	for _, s := range samples {
		payload := make([]byte, s.size)
		if _, err := readAt(d.r, payload, int64(s.offset)); err != nil {
			return model.NewError(model.ErrTruncated, model.StageDemux, id, int64(s.offset), "sample read: %v", err)
		}
		ev := model.Event{
			TrackID: id, Kind: model.EventMedia,
			Ticks:     clock.ConvertSigned(s.pts, tb.timescale, d.opts.Timescale),
			Duration:  int64(clock.Convert(uint64(s.dur), tb.timescale, d.opts.Timescale)),
			SrcOffset: int64(s.offset), Size: s.size, Seq: d.seq, Payload: payload,
		}
		d.seq++
		switch kind {
		case model.TrackSubtitle:
			ev.Kind = model.EventSubtitle
		case model.TrackMeta:
			ev.Kind = model.EventID3
		case model.TrackVideo:
			d.emitSEI(&ev, payload)
		}
		d.events = append(d.events, ev)
	}
	return nil
}

// emitSEI scans an AVCC sample (4-byte NAL lengths) for SEI NAL units and
// appends one SEI event per unit; the media sample payload stays untouched.
func (d *mp4Demuxer) emitSEI(media *model.Event, payload []byte) {
	pos := 0
	for pos+4 <= len(payload) {
		n := int(binary.BigEndian.Uint32(payload[pos:]))
		if n <= 0 || pos+4+n > len(payload) {
			return // not clean AVCC; leave payload as-is
		}
		nal := payload[pos+4 : pos+4+n]
		if len(nal) > 0 && nal[0]&0x1f == 6 {
			cp := make([]byte, len(nal))
			copy(cp, nal)
			d.events = append(d.events, model.Event{
				TrackID: media.TrackID, Kind: model.EventSEI,
				Ticks: media.Ticks, Duration: 0,
				SrcOffset: media.SrcOffset + int64(pos+4), Size: uint32(len(nal)),
				Seq: d.seq, Payload: cp,
			})
			d.seq++
		}
		pos += 4 + n
	}
}
