package index

import (
	"io"
	"os"
	"sort"
	"strings"

	"github.com/abema/go-mp4"

	"replaypipe/internal/timebase"
)

// ParseFile opens path, walks its box tree within limits and builds the full
// presentation model. Parse errors carry stable codes and byte offsets.
func ParseFile(path string, limits Limits) (*Media, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "open %s", path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, timebase.Fail(timebase.CodeInternal, 0, -1).Wrap(err, "stat %s", path)
	}
	p := &parser{
		path:   path,
		f:      f,
		size:   st.Size(),
		limits: limits,
		media: &Media{
			Path:     path,
			FileSize: st.Size(),
			Tracks:   []*Track{},
		},
	}
	if e := p.walk(); e != nil {
		return nil, e
	}
	if len(p.media.Tracks) == 0 {
		return nil, timebase.Newf(timebase.CodeCorruptField, 0, 0,
			"%s contains no supported tracks", path)
	}
	p.media.ContentDigest = digestSamples(p.media.Tracks)
	p.media.EnvelopeDigest = digestEnvelope(p.media.Boxes)
	return p.media, nil
}

type parser struct {
	path     string
	f        *os.File
	size     int64
	limits   Limits
	media    *Media
	total    uint64
	tracks   map[uint32]*Track
	moofBase []moofInfo
	trex     map[uint32]trexDefaults
}

// walk enumerates top-level boxes and descends into the moov/moof trees.
func (p *parser) walk() *timebase.Error {
	off := int64(0)
	for off+8 <= p.size {
		var hdr [8]byte
		if _, err := p.f.ReadAt(hdr[:], off); err != nil {
			return timebase.Fail(timebase.CodeBoxTruncated, 0, off).Wrap(err, "read box header")
		}
		size := int64(hdr[0])<<24 | int64(hdr[1])<<16 | int64(hdr[2])<<8 | int64(hdr[3])
		typ := string(hdr[4:8])
		headerSize := int64(8)
		if size == 1 {
			var ext [8]byte
			if _, err := p.f.ReadAt(ext[:], off+8); err != nil {
				return timebase.Fail(timebase.CodeBoxTruncated, 0, off).Wrap(err, "read largesize")
			}
			size = int64(beU64(ext[:]))
			headerSize = 16
		} else if size == 0 {
			size = p.size - off
		}
		if size < headerSize {
			return timebase.Newf(timebase.CodeBoxTruncated, 0, off,
				"box %q declares size %d smaller than header %d", typ, size, headerSize)
		}
		if off+size > p.size {
			return timebase.Newf(timebase.CodeBoxTruncated, 0, off,
				"box %q size %d exceeds file end %d", typ, size, p.size)
		}
		p.record(0, typ, off, uint64(size), uint64(headerSize), "")
		switch typ {
		case "moov":
			if e := p.parseMoov(off, size); e != nil {
				return e
			}
		case "moof":
			p.moofBase = append(p.moofBase, moofInfo{offset: off, size: size})
		case "mdat":
			p.media.Fragmented = p.media.Fragmented || len(p.moofBase) > 0
		}
		off += size
	}
	if off != p.size {
		return timebase.Newf(timebase.CodeBoxTruncated, 0, off,
			"%d trailing bytes do not form a box header", p.size-off)
	}
	p.media.Fragmented = p.media.Fragmented || len(p.moofBase) > 0
	if p.media.Fragmented {
		if e := p.applyFragments(); e != nil {
			return e
		}
	}
	return p.finishTracks()
}

func (p *parser) record(depth int, typ string, off int64, size, hdr uint64, payloadSum string) {
	p.media.Boxes = append(p.media.Boxes, BoxRecord{
		Depth: depth, Path: typ, Offset: off, Size: size, HeaderSize: hdr, PayloadSum: payloadSum,
	})
}

func (p *parser) addBytes(n uint64, off int64) *timebase.Error {
	p.total += n
	if p.total > p.limits.total() {
		return timebase.Newf(timebase.CodeByteLimit, 0, off,
			"processed %d bytes exceeds limit %d", p.total, p.limits.total())
	}
	return nil
}

// readPayload loads a box payload after enforcing per-box and total limits.
func (p *parser) readPayload(off, size, hdr int64) ([]byte, *timebase.Error) {
	plen := size - hdr
	if plen < 0 {
		return nil, timebase.Newf(timebase.CodeBoxTruncated, 0, off, "negative payload size")
	}
	if uint64(plen) > p.limits.box() {
		return nil, timebase.Newf(timebase.CodeByteLimit, 0, off,
			"box payload %d bytes exceeds per-box limit %d", plen, p.limits.box())
	}
	if e := p.addBytes(uint64(plen), off); e != nil {
		return nil, e
	}
	buf := make([]byte, plen)
	if _, err := p.f.ReadAt(buf, off+hdr); err != nil {
		return nil, timebase.Fail(timebase.CodeBoxTruncated, 0, off).Wrap(err, "read payload")
	}
	return buf, nil
}

// childBoxes enumerates immediate child boxes of a container payload.
func childBoxes(data []byte, base int64) ([]boxView, *timebase.Error) {
	var out []boxView
	i := int64(0)
	for i+8 <= int64(len(data)) {
		size := int64(beU32(data[i:]))
		typ := string(data[i+4 : i+8])
		hdr := int64(8)
		if size == 1 {
			if i+16 > int64(len(data)) {
				return nil, timebase.Newf(timebase.CodeBoxTruncated, 0, base+i, "largesize header truncated")
			}
			size = int64(beU64(data[i+8:]))
			hdr = 16
		} else if size == 0 {
			size = int64(len(data)) - i
		}
		if size < hdr || i+size > int64(len(data)) {
			return nil, timebase.Newf(timebase.CodeBoxTruncated, 0, base+i,
				"child box %q size %d exceeds container", typ, size)
		}
		out = append(out, boxView{
			typ: typ, off: base + i, size: size, hdr: hdr,
			payload: data[i+hdr : i+size],
		})
		i += size
	}
	if i != int64(len(data)) {
		return nil, timebase.Newf(timebase.CodeBoxTruncated, 0, base+i,
			"%d trailing bytes in container", int64(len(data))-i)
	}
	return out, nil
}

type boxView struct {
	typ     string
	off     int64
	size    int64
	hdr     int64
	payload []byte
}

func (b boxView) find(t string) (boxView, bool) {
	for _, c := range b.childrenOrNil() {
		if c.typ == t {
			return c, true
		}
	}
	return boxView{}, false
}

func (b boxView) childrenOrNil() []boxView {
	kids, err := childBoxes(b.payload, b.off+b.hdr)
	if err != nil {
		return nil
	}
	return kids
}

func beU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
func beU64(b []byte) uint64 { return uint64(beU32(b))<<32 | uint64(beU32(b[4:])) }

// decode unmarshals a supported box payload into dst.
func decode(b boxView, dst mp4.IBox) error {
	_, err := mp4.Unmarshal(strings.NewReader(string(b.payload)), uint64(len(b.payload)), dst, mp4.Context{})
	return err
}

func bytesReader(b []byte) io.ReadSeeker { return strings.NewReader(string(b)) }

func (p *parser) finishTracks() *timebase.Error {
	for _, t := range p.media.Tracks {
		if e := p.computeTrack(t); e != nil {
			return e
		}
	}
	sort.SliceStable(p.media.Tracks, func(i, j int) bool {
		return p.media.Tracks[i].ID < p.media.Tracks[j].ID
	})
	return nil
}
