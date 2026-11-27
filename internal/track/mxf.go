package track

import (
	"encoding/binary"
	"io"
	"os"

	"mediaregress/internal/media"
)

// MXF support targets the frame-wrapped operational pattern subset used by the
// regression fixtures: partition packs, one primer pack, the standard metadata
// sets below, and essence elements whose last four key bytes carry the track
// number. Every item keeps its absolute file offset so diagnostics point at the
// first byte that contradicts the profile.
var (
	klvPrefix      = []byte{0x06, 0x0e, 0x2b, 0x34}
	partitionKey   = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01}
	primerKey      = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01, 0x01, 0x05, 0x01, 0x00}
	essencePrefix  = []byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x02, 0x01, 0x01, 0x0d, 0x01, 0x03, 0x01}
	identSetPrefix = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01}
)

const (
	setPreface         byte = 0x2f
	setContentStorage  byte = 0x18
	setMaterialPackage byte = 0x36
	setSourcePackage   byte = 0x37
	setTimelineTrack   byte = 0x3b
	setSequence        byte = 0x0f
	setSourceClip      byte = 0x11
	setGenericPicture  byte = 0x1d
	setCDCI            byte = 0x28
	setWaveAudio       byte = 0x48
)

// Local tags from the SMPTE RP 210 subset this profile interprets.
const (
	tagDataDefinition  = 0x0201
	tagDuration        = 0x0202
	tagInstanceUID     = 0x3c0a
	tagLinkedTrackID   = 0x3006
	tagStoredHeight    = 0x3202
	tagStoredWidth     = 0x3203
	tagSampledHeight   = 0x3204
	tagSampledWidth    = 0x3205
	tagSampledXOffset  = 0x3206
	tagSampledYOffset  = 0x3207
	tagDisplayHeight   = 0x3208
	tagDisplayWidth    = 0x3209
	tagDisplayXOffset  = 0x320a
	tagDisplayYOffset  = 0x320b
	tagColorPrimaries  = 0x3219
	tagTransferChar    = 0x321a
	tagCodingEquations = 0x321b
	tagTrackNumber     = 0x4801
	tagTrackName       = 0x4804
	tagSequenceRef     = 0x4803
	tagEditRate        = 0x4b05
	tagOrigin          = 0x4b02
	tagAudioRate       = 0x3d03
	tagChannelCount    = 0x3d07
	tagQuantBits       = 0x3d01
	tagBlockAlign      = 0x3d0a
)

// klvElem is one top-level KLV element.
type klvElem struct {
	Key        [16]byte
	Offset     int64
	BodyOffset int64 // first value byte
	BodySize   int64
}

// mxfItem is one local-set item with its byte positions.
type mxfItem struct {
	Tag    uint16
	Offset int64 // tag position
	Value  []byte
}

// mxfSet is one metadata set.
type mxfSet struct {
	Kind   byte
	Offset int64
	Items  map[uint16]mxfItem
}

func (s *mxfSet) uint(v uint16) (uint64, bool) {
	it, ok := s.Items[v]
	if !ok {
		return 0, false
	}
	switch len(it.Value) {
	case 1:
		return uint64(it.Value[0]), true
	case 2:
		return uint64(binary.BigEndian.Uint16(it.Value)), true
	case 4:
		return uint64(binary.BigEndian.Uint32(it.Value)), true
	case 8:
		return binary.BigEndian.Uint64(it.Value), true
	}
	return 0, false
}

func (s *mxfSet) bytes(v uint16) ([]byte, bool) {
	it, ok := s.Items[v]
	return it.Value, ok
}

// ParseMXF reads the profile subset from path.
func ParseMXF(path string) (*Input, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "open input", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "stat input", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "rewind input", err)
	}
	digest, err := media.NewDigest(f)
	if err != nil {
		return nil, media.WrapError(media.ClassCorruptInput, media.StageParse, "", 0, "digest input", err)
	}
	in, err := parseMXF(f, fi.Size())
	if in == nil {
		return nil, err
	}
	in.Path = path
	in.Size = fi.Size()
	in.Digest = string(digest)
	if err != nil {
		// The partial input is returned alongside the error so callers can
		// report tracks that were still interpretable before the damage.
		return in, err
	}
	return in, nil
}

func parseMXF(r io.ReaderAt, size int64) (*Input, error) {
	in := &Input{}
	elems, walkErr := walkKLV(r, size)
	if walkErr != nil {
		in.Errors = append(in.Errors, walkErr)
	}
	primer := map[uint16][16]byte{}
	sets := []*mxfSet{}
	essence := map[int64][]klvElem{}
	for _, e := range elems {
		key := e.Key[:]
		switch {
		case hasPrefix(key, primerKey, 14):
			if err := readPrimer(r, e, primer); err != nil {
				in.Errors = append(in.Errors, err)
			}
		case hasPrefix(key, partitionKey, 12):
			// Partition pack value is skipped: the walker already validated
			// its extent, and this profile does not rely on partition indexing.
		case hasPrefix(key, identSetPrefix, 14):
			set, err := readLocalSet(r, e)
			if err != nil {
				in.Errors = append(in.Errors, err)
				continue
			}
			if set == nil {
				// A set-class key with an unknown registry version is not a
				// structure this profile interprets.
				continue
			}
			sets = append(sets, set)
		case hasPrefix(key, essencePrefix, 12):
			trackNum := int64(binary.BigEndian.Uint32(key[12:16]))
			essence[trackNum] = append(essence[trackNum], e)
		}
	}
	if len(sets) == 0 {
		err := media.NewError(media.ClassCorruptInput, media.StageParse, "", 0,
			"no MXF metadata sets found")
		in.Errors = append(in.Errors, err)
		return in, err
	}
	// Structural failures detected while walking KLV stay in in.Errors; track
	// metadata problems are attached to the track they belong to.
	in.Tracks = buildMXFTracks(sets, essence, size)
	return in, nil
}

func hasPrefix(b, prefix []byte, n int) bool {
	if len(b) < n || len(prefix) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if b[i] != prefix[i] {
			return false
		}
	}
	return true
}

// walkKLV reads every top-level KLV element in file order without buffering
// essence payloads. It stops at the first malformed key or length.
func walkKLV(r io.ReaderAt, size int64) ([]klvElem, *media.Error) {
	var out []klvElem
	pos := int64(0)
	for pos < size {
		var key [16]byte
		if size-pos < 16 {
			return out, media.NewError(media.ClassTruncatedInput, media.StageParse, "", size,
				"truncated KLV key at end of file")
		}
		if n, err := r.ReadAt(key[:], pos); n < 16 {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return out, media.WrapError(media.ClassTruncatedInput, media.StageParse, "", pos+int64(n),
				"incomplete KLV key", err)
		}
		length, hdr, berr := readBER(r, pos+16, size)
		if berr != nil {
			return out, berr
		}
		e := klvElem{Key: key, Offset: pos, BodyOffset: pos + 16 + int64(hdr), BodySize: length}
		if length < 0 || e.BodyOffset+length > size {
			return out, media.NewError(media.ClassTruncatedInput, media.StageParse, "", size,
				"KLV value extends past end of file")
		}
		out = append(out, e)
		pos = e.BodyOffset + length
	}
	if len(out) == 0 {
		return out, media.NewError(media.ClassCorruptInput, media.StageParse, "", 0, "empty MXF file")
	}
	return out, nil
}

// readBER decodes a BER length and returns the value plus the encoded width.
func readBER(r io.ReaderAt, at, limit int64) (int64, int, *media.Error) {
	var first [1]byte
	if n, err := r.ReadAt(first[:], at); n < 1 {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return 0, 0, media.WrapError(media.ClassTruncatedInput, media.StageParse, "", at,
			"missing BER length", err)
	}
	if first[0] < 0x80 {
		return int64(first[0]), 1, nil
	}
	n := int(first[0] & 0x7f)
	if n == 0 || n > 8 {
		return 0, 0, media.NewError(media.ClassCorruptInput, media.StageParse, "", at,
			"unsupported BER length form")
	}
	if at+1+int64(n) > limit {
		return 0, 0, media.NewError(media.ClassTruncatedInput, media.StageParse, "", limit,
			"truncated BER length")
	}
	buf := make([]byte, n)
	if got, err := r.ReadAt(buf, at+1); got < n {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return 0, 0, media.WrapError(media.ClassTruncatedInput, media.StageParse, "", at+1+int64(got),
			"incomplete BER length", err)
	}
	var v int64
	for _, b := range buf {
		if v > (1<<55)-1 {
			return 0, 0, media.NewError(media.ClassOutOfRange, media.StageParse, "", at,
				"BER length overflows")
		}
		v = v<<8 | int64(b)
	}
	return v, 1 + n, nil
}

// readPrimer loads the local-tag to UL map. The profile interprets semantics
// from the RP 210 tag table, but requires a primer so the mapping is explicit
// in the file and can be checked for duplicates.
func readPrimer(r io.ReaderAt, e klvElem, dst map[uint16][16]byte) *media.Error {
	if e.BodySize < 8 || (e.BodySize-8)%18 != 0 {
		return media.NewError(media.ClassCorruptInput, media.StageParse, "", e.BodyOffset,
			"primer pack has impossible size")
	}
	buf := make([]byte, e.BodySize)
	if _, err := r.ReadAt(buf, e.BodyOffset); err != nil {
		return media.WrapError(media.ClassTruncatedInput, media.StageParse, "", e.BodyOffset,
			"primer payload truncated", err)
	}
	count := binary.BigEndian.Uint32(buf[0:4])
	itemLen := binary.BigEndian.Uint32(buf[4:8])
	if itemLen != 18 || int(count) != (len(buf)-8)/18 {
		return media.NewError(media.ClassCorruptInput, media.StageParse, "", e.BodyOffset+4,
			"primer item table does not match its count")
	}
	for i := 0; i < int(count); i++ {
		off := 8 + i*18
		tag := binary.BigEndian.Uint16(buf[off:])
		var ul [16]byte
		copy(ul[:], buf[off+2:off+18])
		if _, dup := dst[tag]; dup {
			return media.NewError(media.ClassCorruptInput, media.StageParse, "", e.BodyOffset+int64(off),
				"duplicate primer tag")
		}
		dst[tag] = ul
	}
	return nil
}

// readLocalSet decodes a 2-byte-tag/2-byte-length local set. Lengths are BER so
// values longer than 255 bytes remain representable.
func readLocalSet(r io.ReaderAt, e klvElem) (*mxfSet, *media.Error) {
	// Bytes 0..13 identify a class-1 metadata set, byte 14 is the registry
	// kind and byte 15 the reserved zero. Anything else is not a set this
	// profile reads.
	if e.Key[15] != 0x00 {
		return nil, nil
	}
	kind := e.Key[14]
	buf := make([]byte, e.BodySize)
	if len(buf) > 0 {
		if _, err := r.ReadAt(buf, e.BodyOffset); err != nil {
			return nil, media.WrapError(media.ClassTruncatedInput, media.StageParse, "", e.BodyOffset,
				"metadata set truncated", err)
		}
	}
	set := &mxfSet{Kind: kind, Offset: e.Offset, Items: map[uint16]mxfItem{}}
	pos := 0
	for pos < len(buf) {
		if len(buf)-pos < 4 {
			return set, media.NewError(media.ClassCorruptInput, media.StageParse, "", e.BodyOffset+int64(pos),
				"local set item shorter than its header")
		}
		tag := binary.BigEndian.Uint16(buf[pos:])
		l := int(buf[pos+2])
		// Item header is a 2-byte tag followed by a BER length. Short form is
		// three bytes; long form adds the length bytes.
		width := 3
		if l&0x80 != 0 {
			n := int(l & 0x7f)
			if n == 0 || n > 8 || pos+3+n > len(buf) {
				return set, media.NewError(media.ClassCorruptInput, media.StageParse, "", e.BodyOffset+int64(pos+2),
					"local set item has invalid BER length")
			}
			l = 0
			for i := 0; i < n; i++ {
				l = l<<8 | int(buf[pos+3+i])
			}
			width = 2 + 1 + n
		}
		start := pos + width
		if l < 0 || start+l > len(buf) {
			return set, media.NewError(media.ClassTruncatedInput, media.StageParse, "", e.BodyOffset+int64(start),
				"local set item value runs past its set")
		}
		set.Items[tag] = mxfItem{Tag: tag, Offset: e.BodyOffset + int64(pos), Value: buf[start : start+l]}
		pos = start + l
	}
	return set, nil
}
