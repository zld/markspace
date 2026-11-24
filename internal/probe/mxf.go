package probe

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"archive/internal/clock"
	"archive/internal/fixture"
)

// MXF KLV limits. Values larger than maxLocalSet are rejected so a malformed
// BER length cannot allocate unbounded memory.
const (
	maxLocalSet = 1 << 20
	runInLen    = 65536
)

// mxfParser walks the KLV sequence and reconstructs track metadata.
type mxfParser struct {
	data   []byte
	size   int64
	c      *Container
	tracks []*Track
}

// ProbeMXFFile parses an MXF file from disk.
func ProbeMXFFile(path string) (*Container, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, clock.New(clock.KindInvalidMetadata, "open mxf").WithLocation(path).WithCause(err)
	}
	return ProbeMXF(b)
}

// ProbeMXF parses the synthetic MXF subset: header partition metadata sets,
// an index partition and body essence. Unknown KLV payloads are skipped by
// their declared length; an overrun is a classified truncation error.
func ProbeMXF(data []byte) (*Container, error) {
	p := &mxfParser{data: data, size: int64(len(data)), c: &Container{Format: "mxf"}}
	if err := p.run(); err != nil {
		return nil, err
	}
	if len(p.tracks) == 0 {
		return nil, clock.New(clock.KindInvalidMetadata, "mxf contains no video tracks").
			WithLocation("header partition")
	}
	p.c.Tracks = p.tracks
	for _, t := range p.tracks {
		if t.DurationNS > p.c.DurationNS {
			p.c.DurationNS = t.DurationNS
		}
	}
	return p.c, nil
}

func (p *mxfParser) run() error {
	start := bytes.Index(p.data, []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05})
	if start < 0 {
		return clock.New(clock.KindInvalidMetadata, "no MXF universal label found").
			WithLocation("run-in")
	}
	if start > runInLen {
		return clock.Newf(clock.KindOutOfRange, "run-in length %d exceeds limit", start).
			WithLocation("run-in")
	}
	off := start
	sawIndex := false
	seenRIP := false
	for off < len(p.data) {
		if len(p.data)-off < 16 {
			return clock.Newf(clock.KindTruncated, "%d trailing bytes cannot hold a KLV key", len(p.data)-off).
				WithLocation(fmt.Sprintf("offset %d", off))
		}
		var ul fixture.UL
		copy(ul[:], p.data[off:off+16])
		n, hdr, err := fixture.ParseBERLength(p.data[off+16:])
		if err != nil {
			return clock.Newf(clock.KindTruncated, "bad KLV length: %v", err).
				WithLocation(fmt.Sprintf("offset %d", off)).WithCause(err)
		}
		if n > maxLocalSet && !isPartition(ul) {
			return clock.Newf(clock.KindOutOfRange, "KLV value length %d exceeds limit", n).
				WithLocation(fmt.Sprintf("offset %d", off))
		}
		valueStart := off + 16 + hdr
		valueEnd := valueStart + n
		if valueEnd > len(p.data) {
			return clock.Newf(clock.KindTruncated,
				"KLV %x declares %d bytes, only %d remain", ul[12:16], n, len(p.data)-valueStart).
				WithLocation(fmt.Sprintf("offset %d", off))
		}
		if err := p.handleKLV(ul, p.data[valueStart:valueEnd], off); err != nil {
			return err
		}
		switch {
		case isPartition(ul):
			if ul == fixture.ULIndexPartition {
				sawIndex = true
			}
		case ul == fixture.ULRIP:
			seenRIP = true
		}
		off = valueEnd
	}
	if !seenRIP {
		return clock.New(clock.KindTruncated, "missing RIP; file appears truncated").
			WithLocation(fmt.Sprintf("offset %d", len(p.data)))
	}
	if !sawIndex {
		return clock.New(clock.KindInvalidMetadata, "missing index partition").WithLocation("header")
	}
	return nil
}

func isPartition(ul fixture.UL) bool {
	return ul == fixture.ULHeaderPartition || ul == fixture.ULIndexPartition || ul == fixture.ULBodyPartition
}

func (p *mxfParser) handleKLV(ul fixture.UL, value []byte, off int) error {
	switch ul {
	case fixture.ULIndexTable:
		if len(value) < 8 {
			return clock.New(clock.KindInvalidMetadata, "index table value shorter than 8 bytes").
				WithLocation(fmt.Sprintf("offset %d", off))
		}
		pos := int64(binary.BigEndian.Uint64(value[:8]))
		if pos >= p.size {
			return clock.Newf(clock.KindOutOfRange,
				"index table points to body offset %d beyond EOF %d", pos, p.size).
				WithLocation(fmt.Sprintf("offset %d", off))
		}
	case fixture.ULVideoTrack:
		items, err := parseLocalSet(value, off)
		if err != nil {
			return err
		}
		t := &Track{
			ID:     string(items[fixture.TagTrackID]),
			Format: "mxf",
			Kind:   "video",
			Index:  len(p.tracks),
			Offset: uint64(off),
		}
		if t.ID == "" {
			t.ID = fmt.Sprintf("mxf:%d", t.Index)
		}
		p.tracks = append(p.tracks, t)
	case fixture.ULTimeline:
		items, err := parseLocalSet(value, off)
		if err != nil {
			return err
		}
		cur := p.current()
		if cur == nil {
			return clock.New(clock.KindInvalidMetadata, "timeline set before any track").
				WithLocation(fmt.Sprintf("offset %d", off))
		}
		num, den, err := readRate(items[fixture.TagEditRate])
		if err != nil {
			return err.WithTrack(cur.ID).WithLocation(fmt.Sprintf("offset %d", off))
		}
		cur.Timescale = uint32(num)
		cur.SampleIntervalNS = frameNS(1, num, den)
	case fixture.ULSequence:
		items, err := parseLocalSet(value, off)
		if err != nil {
			return err
		}
		cur := p.current()
		if cur == nil {
			return clock.New(clock.KindInvalidMetadata, "sequence set before any track").
				WithLocation(fmt.Sprintf("offset %d", off))
		}
		frames, cerr := readUint(items[fixture.TagDuration])
		if cerr != nil {
			return cerr.WithTrack(cur.ID).WithLocation(fmt.Sprintf("offset %d", off))
		}
		cur.SampleCount = int(frames)
		cur.DurationUnits = frames
		if cur.SampleIntervalNS > 0 {
			cur.DurationNS = frames * cur.SampleIntervalNS
		}
		clips, cerr := parseClips(items[fixture.TagSequenceRef], cur, off)
		if cerr != nil {
			return cerr
		}
		cur.Clips = clips
	case fixture.ULPictureDesc:
		items, err := parseLocalSet(value, off)
		if err != nil {
			return err
		}
		cur := p.current()
		if cur == nil {
			return clock.New(clock.KindInvalidMetadata, "picture descriptor before any track").
				WithLocation(fmt.Sprintf("offset %d", off))
		}
		if cerr := applyDescriptor(cur, items, off); cerr != nil {
			return cerr
		}
	}
	return nil
}

func (p *mxfParser) current() *Track {
	if len(p.tracks) == 0 {
		return nil
	}
	return p.tracks[len(p.tracks)-1]
}

func applyDescriptor(t *Track, items map[uint16][]byte, off int) *clock.Error {
	loc := fmt.Sprintf("offset %d", off)
	var err *clock.Error
	if t.StoredWidth, err = readInt(items[fixture.TagStoredW]); err != nil {
		return err.WithTrack(t.ID).WithLocation(loc)
	}
	if t.StoredHeight, err = readInt(items[fixture.TagStoredH]); err != nil {
		return err.WithTrack(t.ID).WithLocation(loc)
	}
	if t.DisplayWidth, err = readInt(items[fixture.TagDisplayW]); err != nil {
		return err.WithTrack(t.ID).WithLocation(loc)
	}
	if t.DisplayHeight, err = readInt(items[fixture.TagDisplayH]); err != nil {
		return err.WithTrack(t.ID).WithLocation(loc)
	}
	if v, ok := items[fixture.TagRotation]; ok {
		if len(v) != 1 {
			return clock.New(clock.KindInvalidMetadata, "rotation field must be one byte").
				WithTrack(t.ID).WithLocation(loc)
		}
		t.Rotations = append(t.Rotations, RotationDecl{Degrees: int(v[0]) * 90, Source: SourceMXFRot})
	}
	if v, ok := items[fixture.TagCrop]; ok {
		if len(v) != 8 {
			return clock.New(clock.KindInvalidMetadata, "crop field must be eight bytes").
				WithTrack(t.ID).WithLocation(loc)
		}
		t.Crops = append(t.Crops, CropDecl{
			Left: int(binary.BigEndian.Uint16(v[0:2])), Right: int(binary.BigEndian.Uint16(v[2:4])),
			Top: int(binary.BigEndian.Uint16(v[4:6])), Bottom: int(binary.BigEndian.Uint16(v[6:8])),
			Source: SourceMXFCrop,
		})
	}
	if v, ok := items[fixture.TagColor]; ok {
		if len(v) != 7 {
			return clock.New(clock.KindInvalidMetadata, "color field must be seven bytes").
				WithTrack(t.ID).WithLocation(loc)
		}
		t.Colors = append(t.Colors, ColorDecl{
			Primaries: binary.BigEndian.Uint16(v[0:2]),
			Transfer:  binary.BigEndian.Uint16(v[2:4]),
			Matrix:    binary.BigEndian.Uint16(v[4:6]),
			FullRange: v[6] != 0,
			Source:    SourceMXFCol,
		})
	}
	return nil
}

func parseClips(ref []byte, t *Track, off int) ([]ClipRef, *clock.Error) {
	if len(ref) == 0 {
		return nil, nil
	}
	var clips []ClipRef
	rest := ref
	for len(rest) >= 2 {
		if rest[0] == 0x00 && rest[1] == 0x00 {
			break
		}
		items, consumed, err := parseLocalSetItems(rest, 0)
		if err != nil {
			return nil, err.WithTrack(t.ID).WithLocation(fmt.Sprintf("offset %d", off))
		}
		start, err := readUint(items[fixture.TagClipStart])
		if err != nil {
			return nil, err.WithTrack(t.ID).WithLocation(fmt.Sprintf("offset %d", off))
		}
		count, err := readUint(items[fixture.TagClipDur])
		if err != nil {
			return nil, err.WithTrack(t.ID).WithLocation(fmt.Sprintf("offset %d", off))
		}
		clips = append(clips, ClipRef{
			StartNS:    start * t.SampleIntervalNS,
			DurationNS: count * t.SampleIntervalNS,
		})
		rest = rest[consumed:]
	}
	return clips, nil
}

func parseLocalSet(value []byte, off int) (map[uint16][]byte, *clock.Error) {
	items, _, err := parseLocalSetItems(value, 0)
	if err != nil {
		return nil, err.WithLocation(fmt.Sprintf("offset %d", off))
	}
	return items, nil
}

// parseLocalSetItems decodes tag/length/value items until a 0x0000 terminator
// or the end of the buffer. It returns the bytes consumed.
func parseLocalSetItems(b []byte, depth int) (map[uint16][]byte, int, *clock.Error) {
	if depth > 2 {
		return nil, 0, clock.New(clock.KindOutOfRange, "local set nesting too deep")
	}
	items := make(map[uint16][]byte)
	i := 0
	for i+2 <= len(b) {
		tag := binary.BigEndian.Uint16(b[i : i+2])
		if tag == 0x0000 {
			return items, i + 2, nil
		}
		if i+2 >= len(b) {
			break
		}
		lb := b[i+2]
		var n, consumed int
		if lb&0x80 != 0 {
			k := int(lb & 0x7f)
			if k == 0 || k > 8 || i+3+k > len(b) {
				return nil, 0, clock.New(clock.KindTruncated, "malformed local set length")
			}
			n = 0
			for j := 0; j < k; j++ {
				n = n<<8 | int(b[i+3+j])
			}
			consumed = 3 + k
		} else {
			n = int(lb)
			consumed = 3
		}
		if n < 0 || n > maxLocalSet || i+consumed+n > len(b) {
			return nil, 0, clock.Newf(clock.KindTruncated, "local set item 0x%04x overruns buffer", tag)
		}
		items[tag] = b[i+consumed : i+consumed+n]
		i += consumed + n
	}
	return items, i, nil
}

func readUint(b []byte) (int64, *clock.Error) {
	if len(b) == 0 || len(b) > 8 {
		return 0, clock.Newf(clock.KindInvalidMetadata, "integer field has length %d", len(b))
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	if v > uint64(1)<<62 {
		return 0, clock.New(clock.KindOverflow, "integer field exceeds 62 bits")
	}
	return int64(v), nil
}

func readInt(b []byte) (int, *clock.Error) {
	v, err := readUint(b)
	if err != nil {
		return 0, err
	}
	if v > int64(^uint(0)>>1) {
		return 0, clock.New(clock.KindOverflow, "integer field exceeds platform int")
	}
	return int(v), nil
}

func readRate(b []byte) (int32, int32, *clock.Error) {
	if len(b) != 8 {
		return 0, 0, clock.Newf(clock.KindInvalidMetadata, "edit rate must be 8 bytes, got %d", len(b))
	}
	num := int32(binary.BigEndian.Uint32(b[0:4]))
	den := int32(binary.BigEndian.Uint32(b[4:8]))
	if num <= 0 || den <= 0 {
		return 0, 0, clock.Newf(clock.KindInvalidMetadata, "non-positive edit rate %d/%d", num, den)
	}
	return num, den, nil
}

func frameNS(n int64, num, den int32) int64 {
	if num <= 0 || den <= 0 {
		return 0
	}
	tb, err := clock.NewTimebase(uint32(num))
	if err != nil {
		return 0
	}
	ns, err := tb.ToNanos(n * int64(den))
	if err != nil {
		return 0
	}
	return ns
}

// Sniff determines a container format from its leading bytes and probes it.
func Sniff(data []byte) (*Container, error) {
	if len(data) >= 8 && string(data[4:8]) == "ftyp" {
		return ProbeMP4(bytes.NewReader(data))
	}
	if bytes.Contains(data, []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05}) {
		return ProbeMXF(data)
	}
	// Fall back to the MP4 reader so the error carries a structural offset.
	if _, err := mp4.ReadBoxInfo(bytes.NewReader(data)); err == io.EOF || err == io.ErrUnexpectedEOF {
		return nil, clock.New(clock.KindTruncated, "file shorter than a container header").
			WithLocation(fmt.Sprintf("offset 0 (size %d)", len(data)))
	}
	return nil, clock.New(clock.KindInvalidMetadata, "unrecognised container format").
		WithLocation("offset 0")
}
