package stream

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediasync/internal/issue"
)

// ---------------------------------------------------------------------------
// Structural scan

// scanBoxStructure walks box headers independently of go-mp4 so a malformed
// or truncated size is reported at the exact byte where the file stops making
// sense. Payloads are not decoded here.
func scanBoxStructure(r io.ReadSeeker, input string, fileSize int64) (issue.List, []issue.ByteRange) {
	var issues issue.List
	var damage []issue.ByteRange
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return issues, damage
	}
	add := func(off, end int64, cat issue.Category, detail string) {
		issues.Add(issue.New(cat, input, detail).WithByte(off))
		if end < off {
			end = off
		}
		damage = append(damage, issue.ByteRange{Input: input, Start: off, End: end,
			Categories: []issue.Category{cat}})
	}

	var walk func(off, end int64, depth int)
	walk = func(off, end int64, depth int) {
		for off < end {
			if end-off < 8 {
				add(off, end, issue.MP4Truncated,
					fmt.Sprintf("%d trailing bytes cannot form a box header", end-off))
				return
			}
			if _, err := r.Seek(off, io.SeekStart); err != nil {
				return
			}
			var hdr [8]byte
			if _, err := io.ReadFull(r, hdr[:]); err != nil {
				add(off, end, issue.MP4Truncated, "short box header")
				return
			}
			boxSize := int64(binary.BigEndian.Uint32(hdr[0:4]))
			typ := string(hdr[4:8])
			headerLen := int64(8)
			if boxSize == 1 {
				var ext [8]byte
				if _, err := io.ReadFull(r, ext[:]); err != nil {
					add(off, end, issue.MP4Truncated,
						fmt.Sprintf("truncated 64-bit size for %q", typ))
					return
				}
				boxSize = int64(binary.BigEndian.Uint64(ext[:]))
				headerLen = 16
			} else if boxSize == 0 {
				boxSize = end - off
			}
			if boxSize < headerLen {
				add(off, end, issue.MP4Structure,
					fmt.Sprintf("box %q at %d declares impossible size %d", typ, off, boxSize))
				return
			}
			if off+boxSize > end {
				add(off, end, issue.MP4Truncated,
					fmt.Sprintf("box %q at %d declares %d bytes but only %d remain",
						typ, off, boxSize, end-off))
				return
			}
			if isContainer(typ) && depth < 8 {
				walk(off+headerLen, off+boxSize, depth+1)
			}
			off += boxSize
		}
	}
	walk(0, fileSize, 0)
	return issues, damage
}

func isContainer(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "edts", "dinf", "moof", "traf", "mvex":
		return true
	}
	return false
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

func underTrak(path mp4.BoxPath) bool {
	for _, t := range path {
		if t.String() == "trak" {
			return true
		}
	}
	return false
}

// collectTracks walks the whole tree, expanding containers so nested sample
// tables are reached. Decode failures are reported with the box offset.
func collectTracks(f *os.File, input string, res *MP4Result) []*trakData {
	var traks []*trakData
	var cur *trakData
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		typ := h.BoxInfo.Type
		if typ == mp4.BoxTypeTrak() {
			cur = &trakData{trakOffset: int64(h.BoxInfo.Offset)}
			traks = append(traks, cur)
		}
		if cur != nil && underTrak(h.Path) && !isContainer(typ.String()) && typ != mp4.BoxTypeTrak() {
			box, _, err := h.ReadPayload()
			if err != nil {
				cat := issue.MP4Structure
				if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
					cat = issue.MP4Truncated
				}
				res.Issues.Add(issue.New(cat, input,
					fmt.Sprintf("%s: %v", typ.String(), err)).WithByte(int64(h.BoxInfo.Offset)))
				res.Damaged = append(res.Damaged, issue.ByteRange{
					Input: input, Start: int64(h.BoxInfo.Offset),
					End:        int64(h.BoxInfo.Offset + h.BoxInfo.Size),
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
		}
		if isContainer(typ.String()) {
			if _, err := h.Expand(); err != nil {
				res.Issues.Add(issue.New(issue.MP4Truncated, input,
					fmt.Sprintf("%s: %v", typ.String(), err)).WithByte(int64(h.BoxInfo.Offset)))
				return nil, nil
			}
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		cat := issue.MP4Structure
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			cat = issue.MP4Truncated
		}
		res.Issues.Add(issue.New(cat, input, fmt.Sprintf("box walk: %v", err)).
			WithByte(int64(filesize(f))))
	}
	return traks
}

func filesize(f *os.File) int64 {
	st, err := f.Stat()
	if err != nil {
		return 0
	}
	return st.Size()
}

func pickVideoTrack(traks []*trakData) *trakData {
	for _, t := range traks {
		if t.hdlr != nil && string(t.hdlr.HandlerType[:]) == "vide" {
			return t
		}
	}
	return nil
}

// readBoxBytes returns the raw bytes [off, off+size) for diagnostics/tests.
func readBoxBytes(f io.ReadSeeker, off, size int64) ([]byte, error) {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	_, err := io.ReadFull(f, buf)
	return buf, err
}

// stampInput fills the input name on issues produced by table expansion.
func stampInput(list issue.List, input string) {
	for i := range list {
		if list[i].Loc.Input == "" {
			list[i].Loc.Input = input
		}
		if list[i].Loc.ByteOffset < 0 {
			list[i].Loc.ByteOffset = 0
		}
	}
}
