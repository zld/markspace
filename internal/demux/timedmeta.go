package demux

import (
	"encoding/binary"
	"io"

	"github.com/abema/go-mp4"

	"mediacore/internal/clock"
	"mediacore/internal/model"
)

// Pseudo-track IDs for timed metadata that lives outside media tracks.
const (
	TrackEMSG = "mp4:emsg"
	TrackCHPL = "mp4:chpl"
)

// chplTimescale is the chapter time unit (100 ns, Nero chpl convention).
const chplTimescale = 10000

// emsg converts an emsg box into a unified event on the emsg pseudo-track.
func (d *mp4Demuxer) emsg(bi *mp4.BoxInfo) error {
	var b mp4.Emsg
	if err := d.unmarshal(bi, &b); err != nil {
		return err
	}
	if b.Timescale == 0 {
		return model.NewError(model.ErrBadLength, model.StageDemux, TrackEMSG, int64(bi.Offset), "emsg timescale 0")
	}
	var pts uint64
	if b.Version == 1 {
		pts = b.PresentationTime
	} else {
		pts = uint64(b.PresentationTimeDelta)
	}
	payload := make([]byte, len(b.MessageData))
	copy(payload, b.MessageData)
	d.addPseudoTrack(TrackEMSG, model.TrackMeta, b.Timescale)
	d.events = append(d.events, model.Event{
		TrackID: TrackEMSG, Kind: model.EventEMSG,
		Ticks:     int64(clock.Convert(pts, b.Timescale, d.opts.Timescale)),
		Duration:  int64(clock.Convert(uint64(b.EventDuration), b.Timescale, d.opts.Timescale)),
		SrcOffset: int64(bi.Offset), Size: uint32(len(payload)),
		Seq: d.seq, Payload: payload,
	})
	d.seq++
	return nil
}

// chpl parses a Nero-style chapter box: version(1) flags(3) reserved(4)
// count(2), then per entry start(8, 100ns units) titleLen(1) title.
func (d *mp4Demuxer) chpl(bi *mp4.BoxInfo, payload, boxEnd uint64) error {
	n := boxEnd - payload
	if n > 1<<20 {
		return model.NewError(model.ErrBadLength, model.StageDemux, TrackCHPL, int64(bi.Offset), "chpl too large: %d", n)
	}
	buf := make([]byte, n)
	if _, err := d.r.Seek(int64(payload), io.SeekStart); err != nil {
		return model.NewError(model.ErrIO, model.StageDemux, TrackCHPL, int64(bi.Offset), "seek: %v", err)
	}
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return model.NewError(model.ErrTruncated, model.StageDemux, TrackCHPL, int64(bi.Offset), "chpl body: %v", err)
	}
	if len(buf) < 10 {
		return model.NewError(model.ErrBadLength, model.StageDemux, TrackCHPL, int64(bi.Offset), "chpl body %d bytes", len(buf))
	}
	count := int(binary.BigEndian.Uint16(buf[8:10]))
	off := 10
	d.addPseudoTrack(TrackCHPL, model.TrackMeta, chplTimescale)
	for i := 0; i < count; i++ {
		if off+9 > len(buf) {
			return model.NewError(model.ErrTruncated, model.StageDemux, TrackCHPL, int64(bi.Offset)+int64(off),
				"chpl entry %d truncated", i)
		}
		start := binary.BigEndian.Uint64(buf[off : off+8])
		tlen := int(buf[off+8])
		off += 9
		if off+tlen > len(buf) {
			return model.NewError(model.ErrTruncated, model.StageDemux, TrackCHPL, int64(bi.Offset)+int64(off),
				"chpl title %d truncated", i)
		}
		title := string(buf[off : off+tlen])
		off += tlen
		d.events = append(d.events, model.Event{
			TrackID: TrackCHPL, Kind: model.EventChapter,
			Ticks:     int64(clock.Convert(start, chplTimescale, d.opts.Timescale)),
			SrcOffset: int64(bi.Offset), Size: uint32(9 + tlen),
			Seq: d.seq, Payload: []byte(title),
		})
		d.seq++
	}
	return nil
}

func (d *mp4Demuxer) addPseudoTrack(id string, kind model.TrackKind, timescale uint32) {
	for _, t := range d.tracks {
		if t.ID == id {
			return
		}
	}
	d.tracks = append(d.tracks, model.Track{ID: id, Kind: kind, Timescale: timescale, Handler: "meta"})
}
