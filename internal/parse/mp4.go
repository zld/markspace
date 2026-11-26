package parse

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"replayproof/internal/media"
)

// boxPrefix is the number of payload bytes a container box uses before its
// first child. It is used by the structural scanner, which has to locate
// nested boxes without unmarshalling payloads.
var boxPrefix = map[string]int64{
	"stsd": 8,
	"avc1": 78,
	"encv": 78,
	"mp4a": 28,
	"enca": 28,
	"dref": 8,
}

var containerTypes = map[string]bool{
	"moov": true, "trak": true, "edts": true, "mdia": true, "minf": true,
	"stbl": true, "stsd": true, "dinf": true, "dref": true, "udta": true,
	"avc1": true, "encv": true, "mp4a": true, "enca": true,
}

// maxBoxDepth bounds recursion so a hostile file cannot exhaust the stack.
const maxBoxDepth = 32

// validateStructure walks box headers and reports the exact box offset that
// is truncated or overruns its parent. It intentionally works on raw headers
// so the failure position is available even when higher-level parsing cannot
// continue.
func validateStructure(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return media.Wrap(err, media.CodeIO, "mp4.stat", -1)
	}
	size := fi.Size()
	if err := scanBoxes(f, 0, size, size, 0); err != nil {
		return err
	}
	return nil
}

func scanBoxes(f *os.File, start, end, fileSize int64, depth int) error {
	if depth > maxBoxDepth {
		return media.New(media.CodeInvalidBox, "mp4.scan", start, "box nesting too deep")
	}
	var off int64
	for off = start; off < end; {
		if end-off < 8 {
			return &media.Error{
				Code: media.CodeTruncatedBox, Op: "mp4.scan", Offset: off,
				Msg: fmt.Sprintf("partial box header at file end %d", fileSize),
			}
		}
		var hdr [8]byte
		if _, err := f.ReadAt(hdr[:], off); err != nil {
			return media.Wrap(err, media.CodeIO, "mp4.scan", off)
		}
		boxSize := int64(binary.BigEndian.Uint32(hdr[:4]))
		typ := string(hdr[4:8])
		header := int64(8)
		if boxSize == 1 {
			if end-off < 16 {
				return &media.Error{
					Code: media.CodeTruncatedBox, Op: "mp4.scan", Offset: off,
					Msg: fmt.Sprintf("partial large-size header at file end %d", fileSize),
				}
			}
			var ext [8]byte
			if _, err := f.ReadAt(ext[:], off+8); err != nil {
				return media.Wrap(err, media.CodeIO, "mp4.scan", off)
			}
			n := binary.BigEndian.Uint64(ext[:])
			if n > uint64(end-off) {
				return boxOverrun(off, int64(n), end, typ)
			}
			boxSize = int64(n)
			header = 16
		} else if boxSize == 0 {
			// A zero size means "to end of enclosing box" (top level: EOF).
			boxSize = end - off
		}
		if boxSize < header {
			return &media.Error{
				Code: media.CodeInvalidBox, Op: "mp4.scan", Offset: off,
				Msg: fmt.Sprintf("box %q declares size %d smaller than header %d", typ, boxSize, header),
			}
		}
		if off+boxSize > end {
			return boxOverrun(off, boxSize, end, typ)
		}
		if containerTypes[typ] {
			childStart := off + header + boxPrefix[typ]
			if childStart > off+boxSize {
				return &media.Error{
					Code: media.CodeTruncatedBox, Op: "mp4.scan", Offset: off,
					Msg: fmt.Sprintf("container %q too small for its prefix", typ),
				}
			}
			if err := scanBoxes(f, childStart, off+boxSize, fileSize, depth+1); err != nil {
				return err
			}
		}
		off += boxSize
	}
	if off != end {
		return &media.Error{
			Code: media.CodeTruncatedBox, Op: "mp4.scan", Offset: off,
			Msg: fmt.Sprintf("box extends past enclosing boundary %d", end),
		}
	}
	return nil
}

func boxOverrun(off, boxSize, end int64, typ string) error {
	return &media.Error{
		Code: media.CodeTruncatedBox, Op: "mp4.scan", Offset: off,
		Msg: fmt.Sprintf("box %q declares %d bytes, only %d available", typ, boxSize, end-off),
	}
}

type boxNode struct {
	Info     mp4.BoxInfo
	Path     []string
	Children []*boxNode
	parent   *boxNode
}

func (n *boxNode) child(typ string) *boxNode {
	for _, c := range n.Children {
		if c.Info.Type.String() == typ {
			return c
		}
	}
	return nil
}

func (n *boxNode) walk(typ string, fn func(*boxNode) error) error {
	for _, c := range n.Children {
		if c.Info.Type.String() == typ {
			if err := fn(c); err != nil {
				return err
			}
		}
	}
	return nil
}

// readBoxTree enumerates all known boxes. The returned roots preserve file
// order; nested boxes are attached to the most recent parent at their depth.
func readBoxTree(f *os.File) ([]*boxNode, error) {
	var roots []*boxNode
	byDepth := make(map[int]*boxNode)
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		depth := len(h.Path) - 1
		n := &boxNode{Info: h.BoxInfo}
		for _, t := range h.Path {
			n.Path = append(n.Path, t.String())
		}
		if depth == 0 {
			roots = append(roots, n)
		} else {
			parent := byDepth[depth-1]
			if parent == nil {
				return nil, fmt.Errorf("box %s at depth %d has no parent", h.BoxInfo.Type, depth)
			}
			n.parent = parent
			parent.Children = append(parent.Children, n)
		}
		byDepth[depth] = n
		for k := range byDepth {
			if k > depth {
				delete(byDepth, k)
			}
		}
		if containerTypes[h.BoxInfo.Type.String()] {
			if _, err := h.Expand(); err != nil {
				return nil, media.Wrap(err, media.CodeInvalidBox, "mp4.expand", int64(h.BoxInfo.Offset))
			}
		}
		return nil, nil
	}
	if _, err := mp4.ReadBoxStructure(f, handler); err != nil {
		var me *media.Error
		if errors.As(err, &me) {
			return nil, err
		}
		return nil, media.Wrap(err, media.CodeInvalidBox, "mp4.read", -1)
	}
	return roots, nil
}

func (n *boxNode) payload(f *os.File) ([]byte, error) {
	if _, err := n.Info.SeekToPayload(f); err != nil {
		return nil, media.Wrap(err, media.CodeIO, "mp4.payload", int64(n.Info.Offset))
	}
	buf := make([]byte, n.Info.Size-n.Info.HeaderSize)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, media.Wrap(err, media.CodeIO, "mp4.payload", int64(n.Info.Offset))
	}
	return buf, nil
}

func (n *boxNode) raw(f *os.File) ([]byte, error) {
	if _, err := n.Info.SeekToStart(f); err != nil {
		return nil, media.Wrap(err, media.CodeIO, "mp4.raw", int64(n.Info.Offset))
	}
	buf := make([]byte, n.Info.Size)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, media.Wrap(err, media.CodeIO, "mp4.raw", int64(n.Info.Offset))
	}
	return buf, nil
}

func unmarshalNode(f *os.File, n *boxNode) (mp4.IBox, error) {
	if _, err := n.Info.SeekToPayload(f); err != nil {
		return nil, media.Wrap(err, media.CodeIO, "mp4.unmarshal", int64(n.Info.Offset))
	}
	box, _, err := mp4.UnmarshalAny(f, n.Info.Type, n.Info.Size-n.Info.HeaderSize, n.Info.Context)
	if err != nil {
		return nil, media.Wrap(err, media.CodeInvalidBox, "mp4.unmarshal."+n.Info.Type.String(), int64(n.Info.Offset))
	}
	return box, nil
}

type mp4SampleTable struct {
	sizes   []uint32
	offsets []int64
	durs    []uint32
	ctts    []int64
	key     []bool
}

// ParseMP4 parses a local MP4 file into tracks with sample maps. Metadata,
// timestamps and byte offsets all come from the file; no external data is
// consulted.
func ParseMP4(path string) (*Asset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, media.Wrap(err, media.CodeIO, "mp4.open", -1)
	}
	defer f.Close()

	if err := validateStructure(f); err != nil {
		if me, ok := media.AsError(err); ok {
			me.File = path
		}
		return nil, err
	}
	roots, err := readBoxTree(f)
	if err != nil {
		if me, ok := media.AsError(err); ok {
			me.File = path
		}
		return nil, err
	}
	var moov *boxNode
	for _, r := range roots {
		if r.Info.Type.String() == "moov" {
			moov = r
			break
		}
	}
	if moov == nil {
		return nil, &media.Error{Code: media.CodeNoTrack, Op: "mp4.moov", File: path, Offset: -1, Msg: "moov box not found"}
	}
	asset := &Asset{Source: "mp4", Path: path}
	err = moov.walk("trak", func(trak *boxNode) error {
		t, err := parseTrak(f, path, trak)
		if err != nil {
			return err
		}
		if t != nil {
			asset.Tracks = append(asset.Tracks, t)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(asset.Tracks) == 0 {
		return nil, &media.Error{Code: media.CodeNoTrack, Op: "mp4.trak", File: path, Offset: -1, Msg: "no audio or video tracks"}
	}
	return asset, nil
}
