package mp4wrap

import (
	"bytes"
	"errors"
	"io"

	mp4 "github.com/abema/go-mp4"
)

// ReadSample mirrors Sample for data recovered from an MP4 file.
type ReadSample struct {
	Data     []byte
	KeyFrame bool
	Duration uint32
}

// ReadResult is the recovered single-track structure.
type ReadResult struct {
	Codec     string
	Timescale uint32
	Samples   []ReadSample
}

// ErrUnsupportedContainer means the file lacks the single-track video
// tables this wrapper knows how to interpret.
var ErrUnsupportedContainer = errors.New("unsupported MP4 container structure")

// Read parses a minimal single-track MP4 produced by Write (or any
// equivalent isom file) and recovers sample bytes and timing.
func Read(r io.ReadSeeker) (*ReadResult, error) {
	cap := &capture{}
	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		name := h.Path[len(h.Path)-1].String()
		switch name {
		case "stsd":
			var buf bytes.Buffer
			if _, err := h.ReadData(&buf); err != nil {
				return nil, err
			}
			cap.codec = detectCodec(buf.Bytes())
			return nil, nil
		case "mdat":
			var buf bytes.Buffer
			if _, err := h.ReadData(&buf); err != nil {
				return nil, err
			}
			cap.mdat = buf.Bytes()
			cap.mdatPayloadStart = int64(h.BoxInfo.Offset + h.BoxInfo.HeaderSize)
			return nil, nil
		case "mdhd", "stts", "stsc", "stsz", "stco", "stss":
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			cap.remember(name, box)
			return box, nil
		default:
			// Descend into container boxes; unknown leaves are skipped.
			return h.Expand()
		}
	}
	if _, err := mp4.ReadBoxStructure(r, handler); err != nil {
		return nil, err
	}
	return cap.build()
}

type capture struct {
	codec             string
	mdhd              *mp4.Mdhd
	stts              *mp4.Stts
	stsc              *mp4.Stsc
	stsz              *mp4.Stsz
	stco              *mp4.Stco
	stss              *mp4.Stss
	mdat              []byte
	mdatPayloadStart  int64
}

func (c *capture) remember(name string, box mp4.IBox) {
	switch name {
	case "mdhd":
		c.mdhd = box.(*mp4.Mdhd)
	case "stts":
		c.stts = box.(*mp4.Stts)
	case "stsc":
		c.stsc = box.(*mp4.Stsc)
	case "stsz":
		c.stsz = box.(*mp4.Stsz)
	case "stco":
		c.stco = box.(*mp4.Stco)
	case "stss":
		c.stss = box.(*mp4.Stss)
	}
}

func (c *capture) build() (*ReadResult, error) {
	if c.stts == nil || c.stsz == nil || c.stco == nil || len(c.stco.ChunkOffset) == 0 {
		return nil, ErrUnsupportedContainer
	}
	n := int(c.stsz.SampleCount)
	if n == 0 {
		return nil, ErrUnsupportedContainer
	}
	keySet := map[uint32]bool{}
	if c.stss != nil {
		for _, num := range c.stss.SampleNumber {
			keySet[num] = true
		}
	}

	// Expand stts runs into per-sample durations.
	durations := make([]uint32, 0, n)
	for _, e := range c.stts.Entries {
		for i := uint32(0); i < e.SampleCount && len(durations) < n; i++ {
			durations = append(durations, e.SampleDelta)
		}
	}
	for len(durations) < n {
		if len(durations) > 0 {
			durations = append(durations, durations[len(durations)-1])
		} else {
			durations = append(durations, 0)
		}
	}

	// One chunk (our writer); walk sample sizes from the chunk offset.
	chunkStart := int64(c.stco.ChunkOffset[0])
	base := chunkStart - c.mdatPayloadStart
	if base < 0 || int(base) > len(c.mdat) {
		return nil, ErrUnsupportedContainer
	}
	sizes := c.sampleSizes(n)
	out := make([]ReadSample, 0, n)
	off := int(base)
	for i := 0; i < n; i++ {
		size := int(sizes[i])
		if off+size > len(c.mdat) {
			return nil, ErrUnsupportedContainer
		}
		data := make([]byte, size)
		copy(data, c.mdat[off:off+size])
		out = append(out, ReadSample{
			Data:     data,
			KeyFrame: keySet[uint32(i+1)],
			Duration: durations[i],
		})
		off += size
	}
	res := &ReadResult{Codec: c.codec, Samples: out}
	if c.mdhd != nil {
		res.Timescale = c.mdhd.Timescale
	}
	return res, nil
}

func (c *capture) sampleSizes(n int) []uint32 {
	if c.stsz.SampleSize != 0 {
		out := make([]uint32, n)
		for i := range out {
			out[i] = c.stsz.SampleSize
		}
		return out
	}
	return c.stsz.EntrySize
}

// detectCodec scans the raw stsd payload for the sample-entry 4cc.
func detectCodec(stsdPayload []byte) string {
	if bytes.Contains(stsdPayload, []byte("hvc1")) || bytes.Contains(stsdPayload, []byte("hev1")) {
		return "H265"
	}
	if bytes.Contains(stsdPayload, []byte("avc1")) {
		return "H264"
	}
	return "unknown"
}
