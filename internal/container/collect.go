package container

import (
	"strings"

	"github.com/abema/go-mp4"

	"mediasync/internal/fault"
)

// handleBox captures every table the track needs. Boxes we do not understand
// are expanded by the caller so their children still get visited.
func (t *Track) handleBox(p *Parser, h *mp4.ReadHandle, bi *mp4.BoxInfo, _ string) error {
	switch bi.Type {
	case mp4.BoxTypeTkhd():
		box, _, err := h.ReadPayload()
		if err != nil {
			return p.translate(err)
		}
		t.ID = box.(*mp4.Tkhd).TrackID
		return nil

	case mp4.BoxTypeMdhd():
		box, _, err := h.ReadPayload()
		if err != nil {
			return p.translate(err)
		}
		mdhd := box.(*mp4.Mdhd)
		t.mdhdOffset = int64(bi.Offset)
		t.Timescale = mdhd.Timescale
		t.mdhdDuration = mdhd.GetDuration()
		return nil

	case mp4.BoxTypeHdlr():
		box, _, err := h.ReadPayload()
		if err != nil {
			return p.translate(err)
		}
		hdlr := box.(*mp4.Hdlr)
		t.hdlrOffset = int64(bi.Offset)
		t.HandlerType = string(hdlr.HandlerType[:])
		t.Name = roleFromHdlr(string(hdlr.HandlerType[:]), hdlr.Name)
		return nil

	case mp4.BoxTypeStts():
		box, _, err := h.ReadPayload()
		if err != nil {
			return p.translate(err)
		}
		stts := box.(*mp4.Stts)
		if stts.EntryCount > MaxTableCount {
			return fault.New(fault.ErrLimitExceeded, t.Name, int64(bi.Offset),
				"stts entry count %d exceeds limit", stts.EntryCount)
		}
		t.stts, t.sttsOffset = stts.Entries, int64(bi.Offset)
		return nil

	case mp4.BoxTypeStsz():
		box, _, err := h.ReadPayload()
		if err != nil {
			return p.translate(err)
		}
		stsz := box.(*mp4.Stsz)
		if stsz.SampleCount > MaxTableCount {
			return fault.New(fault.ErrLimitExceeded, t.Name, int64(bi.Offset),
				"stsz sample count %d exceeds limit", stsz.SampleCount)
		}
		t.stsz, t.stszOffset = stsz, int64(bi.Offset)
		return nil

	case mp4.BoxTypeStsc():
		box, _, err := h.ReadPayload()
		if err != nil {
			return p.translate(err)
		}
		stsc := box.(*mp4.Stsc)
		if stsc.EntryCount > MaxTableCount {
			return fault.New(fault.ErrLimitExceeded, t.Name, int64(bi.Offset),
				"stsc entry count %d exceeds limit", stsc.EntryCount)
		}
		t.stsc, t.stscOffset = stsc.Entries, int64(bi.Offset)
		return nil

	case mp4.BoxTypeStco(), mp4.BoxTypeCo64():
		box, _, err := h.ReadPayload()
		if err != nil {
			return p.translate(err)
		}
		t.chunksBoxOff = int64(bi.Offset)
		switch v := box.(type) {
		case *mp4.Stco:
			if v.EntryCount > MaxTableCount {
				return fault.New(fault.ErrLimitExceeded, t.Name, int64(bi.Offset),
					"stco entry count %d exceeds limit", v.EntryCount)
			}
			for _, o := range v.ChunkOffset {
				t.chunks = append(t.chunks, chunkRef{offset: uint64(o)})
			}
		case *mp4.Co64:
			if v.EntryCount > MaxTableCount {
				return fault.New(fault.ErrLimitExceeded, t.Name, int64(bi.Offset),
					"co64 entry count %d exceeds limit", v.EntryCount)
			}
			for _, o := range v.ChunkOffset {
				t.chunks = append(t.chunks, chunkRef{offset: o, co64: true})
			}
		}
		return nil
	}
	return nil
}

func roleFromHdlr(handlerType, name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "dialogue"):
		return "dialogue"
	case strings.Contains(n, "subtitle"):
		return "subtitle"
	case strings.Contains(n, "sfx"):
		return "sfx"
	}
	switch handlerType {
	case "soun":
		return "audio"
	case "text", "sbtl", "subt":
		return "subtitle"
	default:
		if handlerType == "" {
			return "unknown"
		}
		return handlerType
	}
}
