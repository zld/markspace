package mediacore

import (
	"bytes"
	"io"
	"sort"
	"strconv"
	"strings"
)

// parseDASH parses the small MPD subset used by generated fixtures. The XML
// decoder's InputOffset anchors every element at a byte offset.
func parseDASH(asset string, raw []byte) (*Playlist, error) {
	dec := newXMLDecoder(bytes.NewReader(raw))
	pl := &Playlist{AssetID: asset, Format: FormatDASH}
	var cur *TrackDecl
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, WrapMsg(ErrIllegalField, asset, 0, StagePlaylist, dec.InputOffset(),
				"malformed XML: "+err.Error())
		}
		se, ok := tok.(startElement)
		if !ok {
			continue
		}
		off := dec.InputOffset()
		switch se.Name.Local {
		case "MPD":
			if v := attrVal(se, "asset"); v != "" {
				pl.AssetID = v
			}
		case "AdaptationSet":
			a := attrMap(se)
			id, err := reqUint(asset, a, "id", 1, off)
			if err != nil {
				return nil, err
			}
			ts := optUint(a, "timescale")
			if ts == 0 {
				return nil, WrapMsg(ErrZeroTimescale, asset, id, StagePlaylist, off, "zero timescale")
			}
			cur = &TrackDecl{ID: id, Kind: attrOne(a, "kind", "video"), Timescale: ts,
				Version: attrOne(a, "version", "v1"), Rate: optUint(a, "rate"),
				Layout: attrOne(a, "layout", ""), Target: attrOne(a, "targetLayout", ""),
				TargetRate: optUint(a, "targetRate"), ColorPrio: optUint(a, "colorPrio"),
				Line: 1, ByteOff: off}
			if d := attrOne(a, "depends", ""); d != "" {
				for _, p := range strings.Split(d, " ") {
					if p == "" {
						continue
					}
					dep, perr := strconv.ParseUint(p, 10, 32)
					if perr != nil {
						return nil, illegal(asset, "bad depends list: "+d, 1, off)
					}
					cur.DependsOn = append(cur.DependsOn, uint32(dep))
				}
			}
			pl.Tracks = append(pl.Tracks, cur)
		case "S":
			if cur == nil {
				return nil, illegal(asset, "S outside AdaptationSet", 1, off)
			}
			a := attrMap(se)
			ticks, err := reqUint(asset, a, "d", 1, off)
			if err != nil {
				return nil, err
			}
			if ticks == 0 {
				return nil, WrapMsg(ErrIllegalDuration, asset, cur.ID, StagePlaylist, off,
					"zero segment duration")
			}
			ns, serr := ScaleChecked(int64(ticks), cur.Timescale)
			if serr != nil {
				return nil, WrapMsg(ErrOverflow, asset, cur.ID, StagePlaylist, off, serr.Error())
			}
			ref := &SegmentRef{URI: attrOne(a, "uri", ""), Seq: optUint(a, "n"),
				Duration: ns, Timescale: cur.Timescale, Line: 1, ByteOff: off,
				Kind: attrOne(a, "kind", "media"), Attrs: a}
			if ref.URI == "" {
				return nil, illegal(asset, "S missing uri", 1, off)
			}
			cur.Segments = append(cur.Segments, ref)
		}
	}
	if len(pl.Tracks) == 0 {
		return nil, illegal(asset, "no AdaptationSet", 1, 0)
	}
	sort.SliceStable(pl.Tracks, func(i, j int) bool { return pl.Tracks[i].ID < pl.Tracks[j].ID })
	return pl, nil
}
