// Package demux reads MP4/MOV containers and RTP dumps into the unified
// event model. Unsupported structures and truncated data are reported
// with track, raw byte offset and error category; nothing is dropped
// silently.
package demux

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediacore/internal/model"
)

// DemuxMP4 demultiplexes one MP4/MOV file into tracks and unified events.
func DemuxMP4(path string, opts Options) ([]model.Track, []model.Event, error) {
	opts = opts.defaults()
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, model.NewError(model.ErrIO, model.StageDemux, "", -1, "open: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, model.NewError(model.ErrIO, model.StageDemux, "", -1, "stat: %v", err)
	}
	d := &mp4Demuxer{r: f, size: fi.Size(), opts: opts}
	if err := d.walk(0, uint64(fi.Size()), nil, 0); err != nil {
		return nil, nil, err
	}
	return d.tracks, d.events, nil
}

func (d *mp4Demuxer) walk(start, end uint64, tb *trackBuilder, depth int) error {
	if depth > 8 {
		return model.NewError(model.ErrUnsupported, model.StageDemux, "", int64(start), "box nesting too deep")
	}
	pos := start
	for pos < end {
		if _, err := d.r.Seek(int64(pos), io.SeekStart); err != nil {
			return model.NewError(model.ErrIO, model.StageDemux, "", int64(pos), "seek: %v", err)
		}
		bi, err := mp4.ReadBoxInfo(d.r)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return model.NewError(model.ErrTruncated, model.StageDemux, d.trackID(tb), int64(pos), "box header truncated")
			}
			return model.NewError(model.ErrIO, model.StageDemux, d.trackID(tb), int64(pos), "read box: %v", err)
		}
		if bi.Size < bi.HeaderSize {
			return model.NewError(model.ErrBadLength, model.StageDemux, d.trackID(tb), int64(bi.Offset),
				"box size %d < header %d", bi.Size, bi.HeaderSize)
		}
		if bi.Size > d.opts.MaxBoxSize {
			return model.NewError(model.ErrBadLength, model.StageDemux, d.trackID(tb), int64(bi.Offset),
				"box size %d exceeds limit %d", bi.Size, d.opts.MaxBoxSize)
		}
		boxEnd := bi.Offset + bi.Size
		if bi.ExtendToEOF {
			boxEnd = uint64(d.size)
		}
		if boxEnd > uint64(d.size) {
			return model.NewError(model.ErrTruncated, model.StageDemux, d.trackID(tb), int64(bi.Offset),
				"box end %d beyond file size %d", boxEnd, d.size)
		}
		payload := bi.Offset + bi.HeaderSize
		switch {
		case bi.Type == mp4.BoxTypeTrak():
			ntb := &trackBuilder{}
			if err := d.walk(payload, boxEnd, ntb, depth+1); err != nil {
				return err
			}
			if err := d.finalizeTrack(ntb, bi.Offset); err != nil {
				return err
			}
		case bi.Type == mp4.BoxTypeStsd():
			// stsd is a fullbox whose sample entries are child boxes
			// starting after the 8-byte header and entry count.
			if err := d.walk(payload+8, boxEnd, tb, depth+1); err != nil {
				return err
			}
		case containerTypes[bi.Type]:
			if err := d.walk(payload, boxEnd, tb, depth+1); err != nil {
				return err
			}
		default:
			if err := d.leaf(bi, payload, boxEnd, tb); err != nil {
				return err
			}
		}
		pos = boxEnd
	}
	return nil
}

func (d *mp4Demuxer) trackID(tb *trackBuilder) string {
	if tb == nil {
		return ""
	}
	return fmt.Sprintf("mp4:%d", tb.trackID)
}

func (d *mp4Demuxer) unmarshal(bi *mp4.BoxInfo, box mp4.IBox) error {
	if _, err := d.r.Seek(int64(bi.Offset+bi.HeaderSize), io.SeekStart); err != nil {
		return model.NewError(model.ErrIO, model.StageDemux, "", int64(bi.Offset), "seek: %v", err)
	}
	if _, err := mp4.Unmarshal(d.r, bi.Size-bi.HeaderSize, box, mp4.Context{}); err != nil {
		return model.NewError(model.ErrTruncated, model.StageDemux, "", int64(bi.Offset), "box %s: %v", bi.Type, err)
	}
	return nil
}

func (d *mp4Demuxer) leaf(bi *mp4.BoxInfo, payload, boxEnd uint64, tb *trackBuilder) error {
	switch bi.Type {
	case mp4.BoxTypeTkhd():
		var b mp4.Tkhd
		if err := d.unmarshal(bi, &b); err != nil {
			return err
		}
		tb.trackID = b.TrackID
	case mp4.BoxTypeMdhd():
		var b mp4.Mdhd
		if err := d.unmarshal(bi, &b); err != nil {
			return err
		}
		tb.timescale = b.Timescale
	case mp4.BoxTypeHdlr():
		var b mp4.Hdlr
		if err := d.unmarshal(bi, &b); err != nil {
			return err
		}
		tb.handler = string(b.HandlerType[:])
	case mp4.BoxTypeAvc1(), mp4.BoxTypeMp4a(), mp4.BoxTypeWvtt(),
		mp4.StrToBoxType("tx3g"), mp4.StrToBoxType("mett"), mp4.StrToBoxType("metx"):
		if tb != nil {
			tb.entryType = bi.Type.String()
		}
	case mp4.BoxTypeElst():
		var b mp4.Elst
		if err := d.unmarshal(bi, &b); err != nil {
			return err
		}
		if len(b.Entries) > 0 {
			tb.mediaTime = b.Entries[0].MediaTimeV1
			if b.Version == 0 {
				tb.mediaTime = int64(b.Entries[0].MediaTimeV0)
			}
		}
	case mp4.BoxTypeStts():
		b := &mp4.Stts{}
		if err := d.unmarshal(bi, b); err != nil {
			return err
		}
		tb.stts = b
	case mp4.BoxTypeCtts():
		b := &mp4.Ctts{}
		if err := d.unmarshal(bi, b); err != nil {
			return err
		}
		tb.ctts = b
	case mp4.BoxTypeStsz():
		b := &mp4.Stsz{}
		if err := d.unmarshal(bi, b); err != nil {
			return err
		}
		tb.stsz = b
	case mp4.BoxTypeStsc():
		b := &mp4.Stsc{}
		if err := d.unmarshal(bi, b); err != nil {
			return err
		}
		tb.stsc = b
	case mp4.BoxTypeStco():
		var b mp4.Stco
		if err := d.unmarshal(bi, &b); err != nil {
			return err
		}
		for _, o := range b.ChunkOffset {
			tb.chunks = append(tb.chunks, uint64(o))
		}
	case mp4.BoxTypeCo64():
		var b mp4.Co64
		if err := d.unmarshal(bi, &b); err != nil {
			return err
		}
		tb.chunks = append(tb.chunks, b.ChunkOffset...)
	case mp4.BoxTypeEmsg():
		return d.emsg(bi)
	case mp4.BoxTypeMoof(), mp4.BoxTypeMvex():
		return model.NewError(model.ErrUnsupported, model.StageDemux, d.trackID(tb), int64(bi.Offset),
			"fragmented MP4 box %s not supported", bi.Type)
	case mp4.StrToBoxType("chpl"):
		return d.chpl(bi, payload, boxEnd)
	}
	return nil
}
