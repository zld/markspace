package stream

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/abema/go-mp4"

	"mediasync/internal/issue"
)

// ---------------------------------------------------------------------------
// Structural scan

// scanBoxStructure walks every box header independently of go-mp4 so a
// malformed size is detected at the exact byte where the file stops making
// sense. It does not decode payloads.
func scanBoxStructure(r io.ReadSeeker, input string, fileSize int64) (issue.List, []issue.ByteRange) {
	var issues issue.List
	var damage []issue.ByteRange
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return issues, damage
	}
	// Limit traversal depth to guard against pathological nesting.
	var walk func(off, end int64, depth int, parents []string) int64
	walk = func(off, end int64, depth int, parents []string) int64 {
		for off < end {
			if end-off < 8 {
				issues.Add(issue.New(issue.MP4Truncated, input, "partial box header").
					WithByte(off))
				damage = append(damage, issue.ByteRange{Start: off, End: end})
				return end
			}
			if _, err := r.Seek(off, io.SeekStart); err != nil {
				return end
			}
			var hdr [8]byte
			if _, err := io.ReadFull(r, hdr[:]); err != nil {
				issues.Add(issue.New(issue.MP4Truncated, input, "short box header").WithByte(off))
				damage = append(damage, issue.ByteRange{Start: off, End: end})
				return end
			}
			boxSize := int64(be32(hdr[0:4]))
			typ := string(hdr[4:8])
			headerLen := int64(8)
			if boxSize == 1 {
				var ext [8]byte
				if _, err := io.ReadFull(r, ext[:]); err != nil {
					issues.Add(issue.New(issue.MP4Truncated, input, fmt.Sprintf("truncated 64-bit size for %q", typ)).WithByte(off))
					damage = append(damage, issue.ByteRange{Start: off, End: end})
					return end
				}
				boxSize = int64(be64(ext[:]))
				headerLen = 16
			} else if boxSize == 0 {
				boxSize = end - off
			}
			if boxSize < headerLen {
				issues.Add(issue.New(issue.MP4Structure, input,
					fmt.Sprintf("box %q at %d declares impossible size %d", typ, off, boxSize)).WithByte(off))
				damage = append(damage, issue.ByteRange{Start: off, End: end})
				return end
			}
			if off+boxSize > end {
				issues.Add(issue.New(issue.MP4Truncated, input,
					fmt.Sprintf("box %q declares %d bytes but only %d remain", typ, boxSize, end-off)).WithByte(off))
				damage = append(damage, issue.ByteRange{Start: off, End: end})
				return end
			}
			if isContainer(typ) && depth < 8 {
				walk(off+headerLen, off+boxSize, depth+1, append(parents, typ))
			}
			off += boxSize
		}
		return off
	}
	walk(0, fileSize, 0, nil)
	for i := range damage {
		damage[i].Input = input
		damage[i].Categories = []issue.Category{issue.MP4Truncated}
	}
	sort.Sort(issues)
	return issues, damage
}

// stampInput fills the input name on issues produced by table expansion.
func stampInput(list issue.List, input string) {
	for i := range list {
		if list[i].Loc.Input == "" {
			list[i].Loc.Input = input
		}
	}
}

func isContainer(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "dinf", "udta", "moof", "traf", "mvex", "meta":
		return true
	}
	return false
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func be64(b []byte) uint64 {
	return uint64(be32(b[:4]))<<32 | uint64(be32(b[4:8]))
}

// ---------------------------------------------------------------------------
// Track collection via go-mp4

type trakData struct {
	trackID    uint32
	trakOffset int64
	mdhd       *mp4.Mdhd
	hdlr       *mp4.Hdlr
	tkhd       *mp4.Tkhd
	stts       *mp4.Stts
	ctts       *mp4.Ctts
	stss       *mp4.Stss
	stsz       *mp4.Stsz
	stco       *mp4.Stco
	co64       *mp4.Co64
	stsc       *mp4.Stsc
	mdhdOffset int64
	sttsOffset int64
	stszOffset int64
	stssOffset int64
}

func collectTracks(f *os.File, input string, fileSize int64, res *MP4Result) []*trakData {
	var traks []*trakData
	var cur *trakData
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		path := h.Path
		typ := h.BoxInfo.Type
		if typ == mp4.BoxTypeTrak() {
			cur = &trakData{trakOffset: int64(h.BoxInfo.Offset)}
			traks = append(traks, cur)
			return nil, nil
		}
		if cur == nil || len(path) < 2 || path[len(path)-2].String() != "trak" {
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			cat := issue.MP4Structure
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				cat = issue.MP4Truncated
			}
			res.Issues.Add(issue.New(cat, input,
				fmt.Sprintf("%s: %v", typ.String(), err)).WithByte(int64(h.BoxInfo.Offset)))
			res.Damaged = append(res.Damaged, issue.ByteRange{
				Start: int64(h.BoxInfo.Offset), End: int64(h.BoxInfo.Offset + h.BoxInfo.Size),
				Categories: []issue.Category{cat},
			})
			return nil, nil
		}
		switch b := box.(type) {
		case *mp4.Mdhd:
			cur.mdhd, cur.mdhdOffset = b, int64(h.BoxInfo.Offset)
		case *mp4.Hdlr:
			cur.hdlr = b
		case *mp4.Tkhd:
			cur.tkhd, cur.trackID = b, b.TrackID
		case *mp4.Stts:
			cur.stts, cur.sttsOffset = b, int64(h.BoxInfo.Offset)
		case *mp4.Ctts:
			cur.ctts = b
		case *mp4.Stss:
			cur.stss, cur.stssOffset = b, int64(h.BoxInfo.Offset)
		case *mp4.Stsz:
			cur.stsz, cur.stszOffset = b, int64(h.BoxInfo.Offset)
		case *mp4.Stco:
			cur.stco = b
		case *mp4.Co64:
			cur.co64 = b
		case *mp4.Stsc:
			cur.stsc = b
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		cat := issue.MP4Structure
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, mp4.ErrBoxInfoNotFound) {
			cat = issue.MP4Truncated
		}
		res.Issues.Add(issue.New(cat, input, fmt.Sprintf("box walk: %v", err)).WithByte(fileSize))
		return traks
	}
	return traks
}

func pickVideoTrack(traks []*trakData) *trakData {
	for _, t := range traks {
		if t.hdlr != nil && string(t.hdlr.HandlerType[:]) == "vide" {
			return t
		}
	}
	return nil
}
