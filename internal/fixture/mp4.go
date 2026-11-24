// Package fixture generates deterministic, self-contained MP4/MOV samples so
// tests and demos never depend on external media. Samples are written as real
// ISO BMFF boxes through go-mp4's marshaller; corruption is applied at a
// known byte offset afterwards.
package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Sample describes one generated input file.
type Sample struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	Kind           string `json:"kind"`
	Timescale      uint32 `json:"timescale"`
	MediaTimescale uint32 `json:"media_timescale"`
	SampleCount    int    `json:"sample_count"`
	KeyInterval    int    `json:"key_interval"`
	FirstPTS       uint32 `json:"first_pts"`
	CorruptAt      int64  `json:"corrupt_at"`
	Description    string `json:"description"`
}

// Fixture kinds.
const (
	KindNormal          = "normal"
	KindTruncated       = "truncated"
	KindTruncatedHeader = "truncated_header"
	KindBadLength       = "bad_length"
	KindWrap            = "wrap"
	KindRegression      = "regression"
	KindStartOffset     = "start_offset"
	KindDuplicate       = "duplicate"
	KindCombined        = "wrap_regression"
)

// Options controls the synthetic media layout.
type Options struct {
	Name           string
	Kind           string
	Timescale      uint32
	MediaTimescale uint32
	SampleCount    int
	KeyInterval    int
	FirstPTS       uint32
	Step           uint32
	Composition    int32
	Description    string
}

// DefaultOptions returns a 30fps video track with a keyframe every 15 samples.
func DefaultOptions(name, kind string) Options {
	return Options{
		Name:           name,
		Kind:           kind,
		Timescale:      1000,
		MediaTimescale: 90000,
		SampleCount:    90,
		KeyInterval:    15,
		FirstPTS:       0,
		Step:           3000, // 90000/30
		Composition:    0,
		Description:    "30fps synthetic video track",
	}
}

// Degrade describes a presentation timestamp defect to inject.
type Degrade struct {
	AtSample int
	Mode     string
	Amount   int64
}

// Degrade modes.
const (
	DegradeBackward  = "backward"
	DegradeDuplicate = "duplicate"
)

// Build validates opts, writes <name>.mp4 into dir and returns its metadata.
func Build(dir string, opts Options) (*Sample, error) {
	data, err := encode(opts, nil)
	if err != nil {
		return nil, err
	}
	return persist(dir, opts, data, -1)
}

// BuildDegraded writes a file whose presentation timestamps contain the
// requested defect. Decode timestamps stay monotonic; the defect lives in the
// composition offsets so the file remains structurally valid.
func BuildDegraded(dir string, opts Options, d Degrade) (*Sample, error) {
	if d.AtSample < 0 || d.AtSample >= opts.SampleCount {
		return nil, fmt.Errorf("fixture: degrade sample %d out of range", d.AtSample)
	}
	data, err := encode(opts, &d)
	if err != nil {
		return nil, err
	}
	return persist(dir, opts, data, -1)
}

// BuildCorrupt writes a healthy file then applies the named corruption.
func BuildCorrupt(dir string, opts Options, mode string) (*Sample, error) {
	data, err := encode(opts, nil)
	if err != nil {
		return nil, err
	}
	var corruptAt int64
	switch mode {
	case KindTruncated:
		// Cut in the middle of the moov payload: the moov header remains
		// valid but its declared extent runs past EOF.
		off, size, err := locateBox(data, "moov")
		if err != nil {
			return nil, err
		}
		data = data[:off+int64(size/2)]
		corruptAt = off
	case KindTruncatedHeader:
		// Leave a partial trailing box header after a complete moov.
		off, _, err := locateBox(data, "mdat")
		if err != nil {
			return nil, err
		}
		data = data[:off+5]
		corruptAt = off
	case KindBadLength:
		// Rewrite mdat's size with a value far beyond EOF.
		off, _, err := locateBox(data, "mdat")
		if err != nil {
			return nil, err
		}
		binary.BigEndian.PutUint32(data[off:], 0x7FFFFFF0)
		corruptAt = off
	default:
		return nil, fmt.Errorf("fixture: unknown corruption mode %q", mode)
	}
	s, err := persist(dir, opts, data, corruptAt)
	if err != nil {
		return nil, err
	}
	s.Kind = mode
	return s, nil
}

func persist(dir string, opts Options, data []byte, corruptAt int64) (*Sample, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, opts.Name+".mp4")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	return &Sample{
		Name:           opts.Name,
		Path:           path,
		Kind:           opts.Kind,
		Timescale:      opts.Timescale,
		MediaTimescale: opts.MediaTimescale,
		SampleCount:    opts.SampleCount,
		KeyInterval:    opts.KeyInterval,
		FirstPTS:       opts.FirstPTS,
		CorruptAt:      corruptAt,
		Description:    opts.Description,
	}, nil
}

// BuildAll writes the full acceptance fixture set into dir.
func BuildAll(dir string) ([]*Sample, error) {
	var out []*Sample
	add := func(s *Sample, err error) error {
		if err != nil {
			return err
		}
		out = append(out, s)
		return nil
	}

	base := DefaultOptions("normal", KindNormal)
	if err := add(Build(dir, base)); err != nil {
		return nil, err
	}

	trunc := DefaultOptions("truncated", KindTruncated)
	trunc.SampleCount = 40
	trunc.Description = "moov payload cut short; declared extent exceeds EOF"
	if err := add(BuildCorrupt(dir, trunc, KindTruncated)); err != nil {
		return nil, err
	}

	truncHdr := DefaultOptions("truncated_header", KindTruncatedHeader)
	truncHdr.SampleCount = 40
	truncHdr.Description = "trailing box header cut after five bytes"
	if err := add(BuildCorrupt(dir, truncHdr, KindTruncatedHeader)); err != nil {
		return nil, err
	}

	bad := DefaultOptions("bad_length", KindBadLength)
	bad.SampleCount = 40
	bad.Description = "mdat size rewritten past EOF"
	if err := add(BuildCorrupt(dir, bad, KindBadLength)); err != nil {
		return nil, err
	}

	wrap := DefaultOptions("wrap", KindWrap)
	wrap.SampleCount = 60
	wrap.FirstPTS = 0xFFFFD8F0 // 1.5s before the 32-bit wrap
	wrap.Description = "presentation timestamps cross the 32-bit wrap point"
	if err := add(Build(dir, wrap)); err != nil {
		return nil, err
	}

	reg := DefaultOptions("regression", KindRegression)
	reg.SampleCount = 20
	reg.FirstPTS = 89000
	reg.Description = "presentation timestamps step backwards mid-track"
	if err := add(BuildDegraded(dir, reg, Degrade{AtSample: 7, Mode: DegradeBackward, Amount: 6000})); err != nil {
		return nil, err
	}

	off := DefaultOptions("start_offset", KindStartOffset)
	off.SampleCount = 30
	off.FirstPTS = 270000
	off.Description = "non-zero starting presentation time"
	if err := add(Build(dir, off)); err != nil {
		return nil, err
	}

	dup := DefaultOptions("duplicate", KindDuplicate)
	dup.SampleCount = 12
	dup.FirstPTS = 5000
	dup.Description = "a repeated presentation timestamp"
	if err := add(BuildDegraded(dir, dup, Degrade{AtSample: 4, Mode: DegradeDuplicate})); err != nil {
		return nil, err
	}

	comb := DefaultOptions("wrap_regression", KindCombined)
	comb.SampleCount = 50
	comb.FirstPTS = 0xFFFFF000
	comb.Description = "wrap followed by a backward jump"
	if err := add(BuildDegraded(dir, comb, Degrade{AtSample: 20, Mode: DegradeBackward, Amount: 9000})); err != nil {
		return nil, err
	}

	return out, nil
}

// locateBox returns the top-level offset and declared size of box typ.
func locateBox(data []byte, typ string) (offset int64, size uint64, err error) {
	r := bytes.NewReader(data)
	for {
		pos, _ := r.Seek(0, io.SeekCurrent)
		if pos+8 > int64(len(data)) {
			return 0, 0, fmt.Errorf("fixture: box %s not found", typ)
		}
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return 0, 0, err
		}
		sz := uint64(binary.BigEndian.Uint32(hdr[:4]))
		got := string(hdr[4:])
		hdrLen := uint64(8)
		if sz == 1 {
			var ext [8]byte
			if _, err := io.ReadFull(r, ext[:]); err != nil {
				return 0, 0, err
			}
			sz = binary.BigEndian.Uint64(ext[:])
			hdrLen = 16
		}
		if got == typ {
			return pos, sz, nil
		}
		if sz < hdrLen {
			return 0, 0, fmt.Errorf("fixture: invalid box size while scanning for %s", typ)
		}
		if _, err := r.Seek(pos+int64(sz), io.SeekStart); err != nil {
			return 0, 0, err
		}
	}
}

// encode builds the complete MP4 byte stream.
func encode(opts Options, degrade *Degrade) ([]byte, error) {
	if opts.Timescale == 0 || opts.MediaTimescale == 0 {
		return nil, fmt.Errorf("fixture: timescales must be non-zero")
	}
	if opts.SampleCount <= 0 || opts.KeyInterval <= 0 {
		return nil, fmt.Errorf("fixture: sample count and key interval must be positive")
	}
	if opts.Step == 0 {
		opts.Step = opts.MediaTimescale / 30
	}
	layout := buildLayout(opts, degrade)

	ftypLen, err := ftypSize()
	if err != nil {
		return nil, err
	}
	// stco stores a 4-byte offset, so moov length is independent of the
	// value we patch into it. Encode once to learn the length, then again
	// with the true mdat payload offset.
	layout.ChunkOff = 0
	moovBuf, err := marshalMoov(layout, opts.Timescale)
	if err != nil {
		return nil, err
	}
	chunkStart := int64(ftypLen) + int64(len(moovBuf)) + int64(mdatHeaderSize)
	layout.ChunkOff = chunkStart
	moovBuf, err = marshalMoov(layout, opts.Timescale)
	if err != nil {
		return nil, err
	}
	if int64(len(moovBuf)) != chunkStart-int64(ftypLen)-int64(mdatHeaderSize) {
		return nil, fmt.Errorf("fixture: moov size changed while patching stco")
	}

	var out bytes.Buffer
	if _, err := marshalFtyp(&out); err != nil {
		return nil, err
	}
	out.Write(moovBuf)

	payloadLen := opts.SampleCount * sampleSize
	hdr, err := mdatHeader(payloadLen)
	if err != nil {
		return nil, err
	}
	out.Write(hdr)
	payload := make([]byte, payloadLen)
	for i := 0; i < opts.SampleCount; i++ {
		for j := 0; j < sampleSize; j++ {
			payload[i*sampleSize+j] = payloadByte(i, j)
		}
	}
	out.Write(payload)
	// Known-but-ignorable and unknown-yet-legal trailing boxes exercise the
	// scanner's skip path without changing mdat offsets.
	freeBox, unknownBox := extraBoxes()
	out.Write(freeBox)
	out.Write(unknownBox)
	return out.Bytes(), nil
}

const (
	sampleSize     = 4
	mdatHeaderSize = 8
)

// buildLayout derives DTS/PTS/size tables from options.
func buildLayout(opts Options, degrade *Degrade) *trackLayout {
	n := opts.SampleCount
	layout := &trackLayout{
		ID:      1,
		MediaTS: opts.MediaTimescale,
		MovieTS: opts.Timescale,
		Width:   640,
		Height:  360,
	}
	for i := 0; i < n; i++ {
		layout.DTS = append(layout.DTS, uint64(opts.FirstPTS)+uint64(i)*uint64(opts.Step))
	}
	// Presentation time equals decode time plus the (constant) composition
	// offset, then any requested defect is applied.
	for i := 0; i < n; i++ {
		var pts int64 = int64(layout.DTS[i]) + int64(opts.Composition)
		switch {
		case degrade == nil:
		case degrade.Mode == DegradeBackward && i >= degrade.AtSample:
			pts -= degrade.Amount
		case degrade.Mode == DegradeDuplicate && i == degrade.AtSample:
			pts = int64(layout.DTS[i-1]) + int64(opts.Composition)
		}
		layout.PTSRaw = append(layout.PTSRaw, uint32(pts))
	}
	for i := 0; i < n; i++ {
		layout.Sizes = append(layout.Sizes, sampleSize)
		if i%opts.KeyInterval == 0 {
			layout.Keyframes = append(layout.Keyframes, uint32(i+1))
		}
	}
	return layout
}
