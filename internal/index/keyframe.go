package index

import (
	"fmt"
	"os"
	"strings"

	"medtrace/internal/core"

	"github.com/abema/go-mp4"
)

// initInfo is what a fragment's initialization segment declares. For DASH
// each representation carries its own init segment; HLS fMP4 fragments carry
// the same information inline or in a shared init.
type initInfo struct {
	trackID   uint32
	timescale uint32
	kind      core.TrackKind
}

// readInit parses an init segment for its track timescale and type. A missing
// init is not an error: the caller falls back to a timescale declared by the
// manifest or the fragment's own defaults.
func readInit(root, rel string) (*initInfo, *core.Fault) {
	if rel == "" {
		return nil, nil
	}
	abs, err := core.SafeJoin(root, rel)
	if err != nil {
		return nil, core.NewFault(core.KindPath, core.Pos{File: rel, Byte: -1}, "init", err.Error())
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, core.WrapFault(err, core.KindIO, core.Pos{File: rel, Byte: -1}, "init", "open init segment")
	}
	defer f.Close()
	info := &initInfo{}
	bips, err := mp4.ExtractBoxesWithPayload(f, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
	})
	if err != nil {
		return nil, core.WrapFault(err, core.KindBoxParse, core.Pos{File: rel, Byte: -1}, "init", "parse init segment")
	}
	for _, b := range bips {
		switch p := b.Payload.(type) {
		case *mp4.Tkhd:
			info.trackID = p.TrackID
		case *mp4.Mdhd:
			info.timescale = p.Timescale
		case *mp4.Hdlr:
			switch strings.TrimRight(string(p.HandlerType[:]), "\x00") {
			case "vide":
				info.kind = core.Video
			case "soun":
				info.kind = core.Audio
			default:
				info.kind = core.Other
			}
		}
	}
	if info.timescale == 0 {
		return nil, core.NewFault(core.KindBoxParse, core.Pos{File: rel, Byte: -1}, "init", "init segment declares no timescale")
	}
	return info, nil
}

// verify checks that every sample lies inside the file and that the declared
// mdats cover the sample bytes. A sample that runs past EOF is the signature
// of a truncated download; it is reported at the sample's original offset.
func verify(sc *mp4Scan, size int64) *core.Fault {
	var prev int64 = -1
	for i, s := range sc.samples {
		if s.offset+int64(s.size) > size {
			return &core.Fault{
				Kind:   core.KindSegmentTruncated,
				Pos:    core.Pos{Byte: s.offset},
				Op:     "verify",
				Detail: fmt.Sprintf("sample %d ends at %d beyond file size %d", i, s.offset+int64(s.size), size),
			}
		}
		if s.size == 0 {
			return &core.Fault{Kind: core.KindBoxParse, Pos: core.Pos{Byte: s.offset}, Op: "verify",
				Detail: fmt.Sprintf("sample %d has zero size", i)}
		}
		if prev >= 0 && s.offset < prev {
			return &core.Fault{Kind: core.KindBoxParse, Pos: core.Pos{Byte: s.offset}, Op: "verify",
				Detail: "sample data overlaps a previous sample"}
		}
		prev = s.offset + int64(s.size)
	}
	return nil
}
