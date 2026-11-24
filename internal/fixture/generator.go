// Package fixture produces deterministic Matroska-flavoured test streams with
// uncompressed RGB video frames and interleaved float32 PCM audio. No fixture
// data is ever read from outside the repository.
package fixture

import (
	"bytes"
	"encoding/binary"
	"math/rand"

	"mediacheck/ebml"
)

// Default geometry and timing for generated programmes.
const (
	DefaultWidth     = 48
	DefaultHeight    = 27
	DefaultFPS       = 24
	DefaultRate      = 48000
	DefaultChannels  = 2
	DefaultTimeScale = int64(1_000_000) // cluster timecodes in milliseconds
)

// Config describes one synthetic programme master.
type Config struct {
	Seed          int64
	Frames        int
	Width         int
	Height        int
	FPS           int
	SampleRate    int
	Channels      int
	LiveSegment   bool // emit Segment with unknown length
	BadLayout     bool // duplicate/unsupported channel mapping
	ColorConflict bool // track and container carry different colour records
	UnknownColor  bool // inject a non-standard colour enumeration
}

// Defaults fills unset fields with the standard test geometry.
func (c *Config) Defaults() {
	if c.Width == 0 {
		c.Width = DefaultWidth
	}
	if c.Height == 0 {
		c.Height = DefaultHeight
	}
	if c.FPS == 0 {
		c.FPS = DefaultFPS
	}
	if c.SampleRate == 0 {
		c.SampleRate = DefaultRate
	}
	if c.Channels == 0 {
		c.Channels = DefaultChannels
	}
	if c.Frames == 0 {
		c.Frames = 64
	}
}

// FrameSize is the byte size of one uncompressed RGB32 frame.
func (c *Config) FrameSize() int { return c.Width * c.Height * 4 }

// TrackNumber assignments inside the fixture.
const (
	VideoTrackNumber = uint32(1)
	AudioTrackNumber = uint32(2)
	VideoTrackUID    = uint32(101)
	AudioTrackUID    = uint32(202)
)

// Build serializes the whole programme and returns the EBML byte stream.
func Build(c Config) ([]byte, error) {
	c.Defaults()
	rng := rand.New(rand.NewSource(c.Seed))
	var out bytes.Buffer
	writeHeader(&out)
	segStart := out.Len()
	out.Write(ebml.EncodeID(uint32(ebml.IDSegment)))
	if c.LiveSegment {
		us, err := ebml.EncodeUnknownSize(8)
		if err != nil {
			return nil, err
		}
		out.Write(us)
	} else {
		// Reserve a placeholder size VINT (8 bytes) patched at Segment end.
		out.Write(make([]byte, 8))
	}
	writeInfo(&out)
	if c.ColorConflict || c.UnknownColor {
		writeContainerColor(&out, c)
	}
	if err := writeTracks(&out, c); err != nil {
		return nil, err
	}
	samplesPerFrame := c.SampleRate / c.FPS
	for f := 0; f < c.Frames; f++ {
		writeCluster(&out, c, rng, f, samplesPerFrame)
	}
	if !c.LiveSegment {
		patchSegmentSize(&out, segStart)
	}
	return out.Bytes(), nil
}

func writeHeader(out *bytes.Buffer) {
	doc, _ := ebml.BuildElement(ebml.IDDocType, []byte("mediacheck"))
	hdr, _ := ebml.BuildElement(ebml.IDEBMLHeader, doc)
	out.Write(hdr)
}

func writeInfo(out *bytes.Buffer) {
	ts := make([]byte, 8)
	binary.BigEndian.PutUint64(ts, uint64(DefaultTimeScale))
	tsEl, _ := ebml.BuildElement(ebml.IDTimeScale, ts)
	info, _ := ebml.BuildElement(ebml.IDInfo, tsEl)
	out.Write(info)
}

func writeContainerColor(out *bytes.Buffer, c Config) {
	// Container deliberately claims BT.601 while the track claims BT.709.
	payload := colorPayload(6, 6, 6, 1, 0)
	el, _ := ebml.BuildElement(ebml.IDContainerCol, payload)
	out.Write(el)
}

// patchSegmentSize rewrites the placeholder 8-byte size VINT at Segment start.
func patchSegmentSize(out *bytes.Buffer, segStart int) {
	payloadLen := out.Len() - segStart - 12 // 4-byte ID + 8-byte size VINT
	size := make([]byte, 8)
	v := payloadLen | 0x01<<56
	binary.BigEndian.PutUint64(size, uint64(v))
	copy(out.Bytes()[segStart+4:], size)
}
