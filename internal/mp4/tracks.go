package mp4

import (
	"io"

	gomp4 "github.com/abema/go-mp4"

	"mediad/internal/clock"
)

func readTrackHeader(r io.ReadSeeker, trak *gomp4.BoxInfo, tr *RawTrack) error {
	bis, err := gomp4.ExtractBoxesWithPayload(r, trak, []gomp4.BoxPath{p("tkhd")})
	if err != nil {
		return wrapParse(err, int64(trak.Offset), tr.TrackID)
	}
	if len(bis) != 1 {
		return clock.New(clock.StageScan, clock.CodeBadManifest, "trak at %d has %d tkhd", trak.Offset, len(bis)).WithOffset(int64(trak.Offset))
	}
	tkhd := bis[0].Payload.(*gomp4.Tkhd)
	tr.TrackID = tkhd.TrackID
	return nil
}

func readMediaHeader(r io.ReadSeeker, trak *gomp4.BoxInfo, tr *RawTrack) error {
	bis, err := gomp4.ExtractBoxesWithPayload(r, trak, []gomp4.BoxPath{p("mdia", "mdhd"), p("mdia", "hdlr")})
	if err != nil {
		return wrapParse(err, int64(trak.Offset), tr.TrackID)
	}
	if len(bis) == 0 {
		return clock.New(clock.StageScan, clock.CodeBadManifest, "track %d has no mdia/mdhd", tr.TrackID).WithOffset(int64(trak.Offset))
	}
	for _, b := range bis {
		switch v := b.Payload.(type) {
		case *gomp4.Mdhd:
			tr.Timescale = v.Timescale
			if v.Version == 1 {
				tr.DurationTicks = int64(v.DurationV1)
			} else {
				tr.DurationTicks = int64(v.DurationV0)
			}
			if tr.Timescale == 0 {
				return clock.New(clock.StageScan, clock.CodeBadTimescale, "track %d has zero media timescale", tr.TrackID).
					WithOffset(int64(b.Info.Offset)).WithTrack(tr.TrackID)
			}
		case *gomp4.Hdlr:
			tr.Kind = handlerKind(v.HandlerType)
			tr.HandlerOffset = int64(b.Info.Offset)
		}
	}
	return nil
}

func handlerKind(t [4]byte) string {
	switch string(t[:]) {
	case "vide":
		return "video"
	case "soun":
		return "audio"
	case "text", "sbtl", "subp":
		return "text"
	default:
		return "other"
	}
}

func readEditList(r io.ReadSeeker, trak *gomp4.BoxInfo, tr *RawTrack) error {
	bis, err := gomp4.ExtractBoxesWithPayload(r, trak, []gomp4.BoxPath{p("edts", "elst")})
	if err != nil {
		return wrapParse(err, int64(trak.Offset), tr.TrackID)
	}
	tr.EditList = clock.EditList{MediaTimescale: tr.Timescale, MovieTimescale: tr.MovieTimescale}
	if len(bis) == 0 {
		return nil
	}
	b := bis[0]
	elst := b.Payload.(*gomp4.Elst)
	tr.TableBytes.Elst = int64(b.Info.Offset)
	if int(elst.EntryCount) > MaxEditEntries {
		return clock.New(clock.StageScan, clock.CodeOversizedTable,
			"elst declares %d entries (limit %d)", elst.EntryCount, MaxEditEntries).
			WithOffset(int64(b.Info.Offset)).WithTrack(tr.TrackID)
	}
	entries := make([]clock.EditEntry, 0, len(elst.Entries))
	// Each ElstEntry is 12 bytes (v0) or 20 bytes (v1) and begins after the
	// full-box header (4) and entry count (4).
	entrySize := int64(12)
	if elst.Version == 1 {
		entrySize = 20
	}
	base := int64(b.Info.Offset+b.Info.HeaderSize) + 8
	for i, e := range elst.Entries {
		var dur uint64
		var mt int64
		if elst.Version == 1 {
			dur = e.SegmentDurationV1
			mt = e.MediaTimeV1
		} else {
			dur = uint64(e.SegmentDurationV0)
			mt = int64(e.MediaTimeV0)
		}
		entries = append(entries, clock.EditEntry{
			SegmentDuration: dur,
			MediaTime:       mt,
			Rate:            int32(e.MediaRateInteger),
			RawOffset:       base + int64(i)*entrySize,
		})
	}
	tr.EditList.Entries = entries
	return nil
}
