// Package mp4 parses ISO base media files with byte-accurate damage handling.
//
// The walker is intentionally separate from go-mp4's structural reader: it
// must know the exact byte offset of every malformed length, keep parsing past
// a damaged sibling, and report truncation without discarding readable
// samples. Payload decoding of known leaf boxes is delegated to go-mp4.
package mp4

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"

	isom "github.com/abema/go-mp4"

	"github.com/pairwise/mediad/internal/fault"
)

// Box is one parsed box header with its readable payload extent.
type Box struct {
	Type         string
	Offset       int64
	Size         int64 // declared size (header included)
	DeclaredSize int64 // alias of Size, kept when truncated
	HeaderSize   int64
	PayloadOff   int64
	PayloadSize  int64 // readable payload bytes, may be < Size-HeaderSize
	Extended     bool  // uses a 64-bit size field
	ExtendToEOF  bool  // declared size 0
	Truncated    bool
	Children     []*Box
	TrackID      uint32
}

// End returns the declared end offset of the box.
func (b *Box) End() int64 { return b.Offset + b.Size }

// ReadableEnd returns the last byte offset that exists in the file.
func (b *Box) ReadableEnd() int64 { return b.PayloadOff + b.PayloadSize }

// MaxFileSize is the largest input the pipeline accepts. Offsets and sample
// spans stay well inside int64, and refusing oversized files keeps a runaway
// path from turning into multi-terabyte reads.
const MaxFileSize int64 = 2 << 30

// Tree is the result of a structural scan.
type Tree struct {
	Path     string
	Size     int64
	Boxes    []*Box
	Warnings []*fault.Error
	r        io.ReaderAt
	closer   io.Closer
}

// Close releases the underlying file opened by Open.
func (t *Tree) Close() error {
	if t.closer != nil {
		err := t.closer.Close()
		t.closer = nil
		return err
	}
	return nil
}

// ReadPayload copies the readable payload of b.
func (t *Tree) ReadPayload(b *Box) ([]byte, error) {
	if b.PayloadSize < 0 || b.PayloadSize > 1<<31 {
		return nil, fault.Errorf("mp4.payload", fault.KindParse, "unreasonable payload size %d", b.PayloadSize).
			WithPath(t.Path).At(b.PayloadOff)
	}
	buf := make([]byte, b.PayloadSize)
	if _, err := t.r.ReadAt(buf, b.PayloadOff); err != nil {
		return nil, fault.New("mp4.payload", fault.KindIO, err).
			WithPath(t.Path).At(b.PayloadOff).WithTrack(b.TrackID)
	}
	return buf, nil
}

// ReadAt copies n bytes at absolute offset.
func (t *Tree) ReadAt(off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || off+n > t.Size {
		return nil, fault.Errorf("mp4.read", fault.KindParse, "range [%d,%d) outside file size %d", off, off+n, t.Size).
			WithPath(t.Path).At(off)
	}
	buf := make([]byte, n)
	if _, err := t.r.ReadAt(buf, off); err != nil {
		return nil, fault.New("mp4.read", fault.KindIO, err).WithPath(t.Path).At(off)
	}
	return buf, nil
}

// Open scans a file from disk. Structural damage is reported in Warnings and
// never prevents the readable part of the tree from being returned.
func Open(path string) (*Tree, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fault.New("mp4.open", fault.KindIO, err).WithPath(path)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fault.New("mp4.stat", fault.KindIO, err).WithPath(path)
	}
	if st.Size() > MaxFileSize {
		f.Close()
		return nil, fault.Errorf("mp4.open", fault.KindParse,
			"file is %d bytes, above the %d byte limit", st.Size(), MaxFileSize).
			WithPath(path).At(0).Fatal()
	}
	t := Scan(path, f, st.Size())
	t.closer = f
	return t, nil
}

// Scan walks r. It returns a tree even when headers are damaged.
func Scan(path string, r io.ReaderAt, size int64) *Tree {
	t := &Tree{Path: path, Size: size, r: r}
	if size < 0 {
		size = 0
	}
	t.Boxes = t.scanRange(0, size, true, nil)
	t.annotateTracks()
	t.annotateWarnings()
	return t
}

var containerTypes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true,
	"edts": true, "dinf": true, "udta": true, "mvex": true, "moof": true,
	"traf": true, "mfra": true, "tref": true, "meta": true, "ilst": true,
}

func (t *Tree) scanRange(start, end int64, root bool, parent *Box) []*Box {
	var out []*Box
	off := start
	for off < end {
		if end-off < 8 {
			t.warn(fault.Errorf("mp4.scan", fault.KindTruncated,
				"%d trailing bytes cannot hold a box header", end-off).At(off).Recovered())
			break
		}
		b, next, err := t.readHeader(off, end)
		if err != nil {
			e, _ := fault.As(err)
			if e == nil {
				e = fault.New("mp4.scan", fault.KindParse, err).At(off)
			}
			t.warn(e.Recovered())
			break // the remainder cannot be trusted as sibling headers
		}
		if parent != nil {
			b.TrackID = parent.TrackID
		}
		if b.Type == "stsd" {
			b.Children = t.scanStsd(b)
		} else if containerTypes[b.Type] {
			b.Children = t.scanRange(b.PayloadOff, b.ReadableEnd(), false, b)
		}
		out = append(out, b)
		if b.Truncated {
			break
		}
		off = next
	}
	return out
}

func (t *Tree) readHeader(off, limit int64) (*Box, int64, error) {
	var hdr [16]byte
	if av := limit - off; av < 8 {
		return nil, 0, fault.Errorf("mp4.box", fault.KindTruncated, "box header overruns container").At(off)
	}
	if _, err := t.r.ReadAt(hdr[:8], off); err != nil {
		return nil, 0, fault.New("mp4.box", fault.KindIO, err).At(off)
	}
	size32 := binary.BigEndian.Uint32(hdr[0:4])
	typ := string(hdr[4:8])
	b := &Box{Type: typ, Offset: off, HeaderSize: 8}
	switch size32 {
	case 0:
		b.ExtendToEOF = true
		b.Size = limit - off
	case 1:
		if limit-off < 16 {
			return nil, 0, fault.Errorf("mp4.box", fault.KindTruncated, "extended size header overruns container").At(off)
		}
		if _, err := t.r.ReadAt(hdr[8:16], off+8); err != nil {
			return nil, 0, fault.New("mp4.box", fault.KindIO, err).At(off)
		}
		b.Extended = true
		b.HeaderSize = 16
		v := binary.BigEndian.Uint64(hdr[8:16])
		if v > uint64(^uint64(0)>>1) {
			return nil, 0, fault.Errorf("mp4.box", fault.KindParse, "extended size overflows int64").At(off)
		}
		b.Size = int64(v)
		if b.Size < 16 {
			return nil, 0, fault.Errorf("mp4.box", fault.KindParse, "extended size %d smaller than header", b.Size).At(off)
		}
	default:
		b.Size = int64(size32)
		if b.Size < 8 {
			return nil, 0, fault.Errorf("mp4.box", fault.KindParse, "declared size %d smaller than header", b.Size).At(off)
		}
	}
	if !plausibleType(typ) {
		return nil, 0, fault.Errorf("mp4.box", fault.KindParse, "implausible box type %q", typ).At(off)
	}
	b.DeclaredSize = b.Size
	b.PayloadOff = off + b.HeaderSize
	declaredEnd := off + b.Size
	readableEnd := declaredEnd
	if declaredEnd > limit {
		readableEnd = limit
		b.Truncated = true
		t.warn(fault.Errorf("mp4.box", fault.KindTruncated,
			"box %q declares %d bytes but only %d readable", typ, b.Size, limit-off).
			At(off).Recovered())
	}
	if readableEnd < b.PayloadOff {
		b.PayloadSize = 0
	} else {
		b.PayloadSize = readableEnd - b.PayloadOff
	}
	if b.Truncated && b.Type == "mdat" {
		b.PayloadSize = readableEnd - b.PayloadOff
	}
	next := b.PayloadOff + b.PayloadSize
	if b.Truncated {
		next = readableEnd
	}
	return b, next, nil
}

// scanStsd parses the full-box header, entry count and sample entries.
func (t *Tree) scanStsd(b *Box) []*Box {
	if b.PayloadSize < 8 {
		t.warn(fault.Errorf("mp4.stsd", fault.KindTruncated, "stsd payload %d bytes lacks entry count", b.PayloadSize).
			At(b.PayloadOff).Recovered())
		return nil
	}
	var hdr [8]byte
	if _, err := t.r.ReadAt(hdr[:], b.PayloadOff); err != nil {
		t.warn(fault.New("mp4.stsd", fault.KindIO, err).At(b.PayloadOff).Recovered())
		return nil
	}
	count := int64(binary.BigEndian.Uint32(hdr[4:8]))
	if count > 64 {
		t.warn(fault.Errorf("mp4.stsd", fault.KindParse, "implausible entry count %d", count).
			At(b.PayloadOff).Recovered())
		return nil
	}
	return t.scanRange(b.PayloadOff+8, b.ReadableEnd(), false, b)
}

func (t *Tree) warn(e *fault.Error) {
	if e == nil {
		return
	}
	e.Path = t.Path
	t.Warnings = append(t.Warnings, e)
}

func plausibleType(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// Walk visits boxes depth-first.
func Walk(boxes []*Box, fn func(*Box) bool) {
	for _, b := range boxes {
		if !fn(b) {
			return
		}
		Walk(b.Children, fn)
	}
}

// Find returns all boxes with the given type, depth-first.
func (t *Tree) Find(typ string) []*Box {
	var out []*Box
	Walk(t.Boxes, func(b *Box) bool {
		if b.Type == typ {
			out = append(out, b)
		}
		return true
	})
	return out
}

// First returns the first box of a type.
func (t *Tree) First(typ string) *Box {
	var found *Box
	Walk(t.Boxes, func(b *Box) bool {
		if b.Type == typ && found == nil {
			found = b
			return false
		}
		return true
	})
	return found
}

// Child returns the first direct child of b with the given type.
func Child(b *Box, typ string) *Box {
	if b == nil {
		return nil
	}
	for _, c := range b.Children {
		if c.Type == typ {
			return c
		}
	}
	return nil
}

func (t *Tree) annotateTracks() {
	for _, moov := range t.Find("moov") {
		for _, trak := range moov.Children {
			if trak.Type != "trak" {
				continue
			}
			id := t.trackIDOf(trak)
			Walk([]*Box{trak}, func(b *Box) bool { b.TrackID = id; return true })
		}
	}
}

func (t *Tree) trackIDOf(trak *Box) uint32 {
	for _, c := range trak.Children {
		if c.Type == "tkhd" {
			return t.trackIDFromTkhd(c)
		}
	}
	return 0
}

func (t *Tree) trackIDFromTkhd(b *Box) uint32 {
	if b.PayloadSize < 16 {
		return 0
	}
	buf := make([]byte, b.PayloadSize)
	if _, err := t.r.ReadAt(buf, b.PayloadOff); err != nil {
		return 0
	}
	if buf[0] == 1 {
		if len(buf) < 24 {
			return 0
		}
		return binary.BigEndian.Uint32(buf[20:24])
	}
	return binary.BigEndian.Uint32(buf[12:16])
}

func (t *Tree) annotateWarnings() {
	for _, w := range t.Warnings {
		if w.TrackID != 0 || w.Offset < 0 {
			continue
		}
		Walk(t.Boxes, func(b *Box) bool {
			if b.Type != "trak" {
				return true
			}
			if w.Offset >= b.Offset && w.Offset < b.ReadableEnd() {
				w.TrackID = b.TrackID
				return false
			}
			return true
		})
	}
}

// Parse decodes b's payload into its typed go-mp4 representation. Truncated
// boxes and unregistered types report ErrUnsupported instead of panicking.
func (t *Tree) Parse(b *Box) (isom.IBox, error) {
	if b.Truncated && b.PayloadSize < b.Size-b.HeaderSize {
		return nil, fault.Errorf("mp4.parse", fault.KindTruncated, "box %q truncated", b.Type).
			WithPath(t.Path).At(b.Offset).WithTrack(b.TrackID).Wrapped(fault.ErrTruncated)
	}
	if !isom.StrToBoxType(b.Type).IsSupported(isom.Context{}) {
		return nil, fault.Errorf("mp4.parse", fault.KindParse, "unsupported box %q", b.Type).
			WithPath(t.Path).At(b.Offset).Wrapped(errUnsupported)
	}
	buf, err := t.ReadPayload(b)
	if err != nil {
		return nil, err
	}
	box, _, err := isom.UnmarshalAny(bytes.NewReader(buf), isom.StrToBoxType(b.Type), uint64(len(buf)), isom.Context{})
	if err != nil {
		return nil, fault.New("mp4.parse", fault.KindParse, err).
			WithPath(t.Path).At(b.Offset).WithTrack(b.TrackID)
	}
	return box, nil
}

var errUnsupported = errors.New("unsupported box")

// String implements fmt.Stringer for diagnostics.
func (b *Box) String() string {
	return fmt.Sprintf("%s@%d+%d", b.Type, b.Offset, b.Size)
}
