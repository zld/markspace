package fixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

// frame is one synthetic media sample before muxing.
type frame struct {
	index    int
	decode   uint64
	duration uint32
	key      bool
	payload  []byte
}

// repFrames returns every frame of a representation in decode order. The
// representation timescale equals the plan timebase, so decode times are plan
// ticks directly.
func (c *Config) repFrames(rep RepConfig) []frame {
	per := c.FramesPerSegment()
	total := per * c.Segments
	gap := rep.KeyframeGap
	if gap <= 0 {
		gap = c.Keyframe
	}
	out := make([]frame, total)
	for i := 0; i < total; i++ {
		out[i] = frame{
			index:    i,
			decode:   uint64(rep.InitialOffset) + uint64(i)*uint64(c.FrameTicks),
			duration: uint32(c.FrameTicks),
			key:      i%gap == 0,
			payload:  payloadFor(c.Seed, rep.ID, i),
		}
	}
	return out
}

// payloadFor deterministically derives a sample payload.
func payloadFor(seed int64, rep string, frameIndex int) []byte {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d/%s/%d", seed, rep, frameIndex)))
	size := 48 + int(h[0])%64
	buf := make([]byte, size)
	binary.BigEndian.PutUint32(buf[0:], uint32(frameIndex))
	binary.BigEndian.PutUint32(buf[4:], uint32(frameIndex)*3600)
	copy(buf[8:], h[:])
	for i := 40; i < size; i++ {
		buf[i] = h[i%32] ^ byte(i)
	}
	return buf
}

// buildSegment muxes frames into a CMAF media segment (styp+moof+mdat).
func buildSegment(seq uint32, frames []frame) ([]byte, error) {
	frag, err := mp4.CreateFragment(seq, 1)
	if err != nil {
		return nil, err
	}
	for _, fr := range frames {
		flags := mp4.SetNonSyncSampleFlags(0)
		flags |= 1 << 24 // sample_depends_on=1 (depends on others)
		if fr.key {
			flags = mp4.SetSyncSampleFlags(0)
			flags |= 2 << 24 // sample_depends_on=2 (does not depend)
		}
		frag.AddFullSample(mp4.FullSample{
			Sample:     mp4.Sample{Flags: flags, Dur: fr.duration, Size: uint32(len(fr.payload))},
			DecodeTime: fr.decode,
			Data:       fr.payload,
		})
	}
	var body bytes.Buffer
	if err := frag.Encode(&body); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := mp4.CreateStyp().Encode(&out); err != nil {
		return nil, err
	}
	out.Write(body.Bytes())
	return out.Bytes(), nil
}

// buildInit muxes the initialization segment for one representation.
func buildInit(timescale int64) ([]byte, error) {
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(uint32(timescale), "video", "und")
	var out bytes.Buffer
	if err := init.Encode(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
