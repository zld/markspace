// Package fixture generates deterministic synthetic bitstreams, keyframe
// indexes and RTP source files used by the demo command and the test suite.
// No external media is referenced: every byte is produced from integer seeds
// so repeated generation yields identical inputs and manifests.
package fixture

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"gamepack/internal/stream"
)

// Injection faults the generator can introduce.
type Injection string

const (
	// InjectSwitch inserts a declared source switch with a backward RTP clock.
	InjectSwitch Injection = "switch"
	// InjectWrap makes the RTP clock cross the half-period and wrap low.
	InjectWrap Injection = "wrap"
	// InjectTruncated writes a unit whose declared length runs past EOF.
	InjectTruncated Injection = "truncated"
)

// Options controls one rendition's generated artifacts.
type Options struct {
	Codec       stream.Codec
	BitrateKbps int
	Units       int  // total coding units
	KeyEvery    int  // keyframe every N units
	WrapBits    uint // RTP clock width used for wrap injection
	Injections  map[Injection]bool
}

// Artifact paths produced for one rendition.
type Artifact struct {
	Codec       stream.Codec
	BitrateKbps int
	StreamPath  string
	IndexPath   string
	RTPPath     string
	// KeyOrdinals are the unit ordinals that carry keyframes.
	KeyOrdinals []int
	// TruncatedPath holds a deliberately broken stream/index pair, when
	// requested, kept separate from the packable artifacts.
	TruncatedPath  string
	TruncatedIndex string
}

// Frame tick spacing for the synthetic 30 fps content: 90000/30 = 3000.
const frameTick uint32 = 3000

// Generate writes one rendition's bitstream, index and RTP files into dir.
func Generate(dir string, opt Options) (*Artifact, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	stem := filepath.Join(dir, string(opt.Codec)+"-"+itoa(opt.BitrateKbps))
	a := &Artifact{
		Codec: opt.Codec, BitrateKbps: opt.BitrateKbps,
		StreamPath: stem + ".bits", IndexPath: stem + ".idx.json", RTPPath: stem + ".rtp",
	}

	raw, units, keys := buildBitstream(opt)
	if err := os.WriteFile(a.StreamPath, raw, 0o600); err != nil {
		return nil, err
	}
	a.KeyOrdinals = keys

	idx := stream.Index{Codec: opt.Codec, Entries: make([]stream.IndexEntry, 0, len(keys))}
	for _, k := range keys {
		u := units[k]
		idx.Entries = append(idx.Entries, stream.IndexEntry{
			Offset: u.start, Length: u.length,
		})
	}
	idxBytes, err := json.MarshalIndent(&idx, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(a.IndexPath, idxBytes, 0o600); err != nil {
		return nil, err
	}

	timestamps := buildTimestamps(opt)
	if err := writeRTP(a.RTPPath, units, timestamps); err != nil {
		return nil, err
	}

	if opt.Injections[InjectTruncated] {
		if err := writeTruncated(stem, raw); err != nil {
			return nil, err
		}
		a.TruncatedPath = stem + ".truncated.bits"
		a.TruncatedIndex = stem + ".truncated.idx.json"
	}
	return a, nil
}

// buildTimestamps returns the raw RTP timestamp for each unit, applying the
// switch and wrap injections deterministically.
func buildTimestamps(opt Options) []uint32 {
	n := opt.Units
	ts := make([]uint32, n)
	switchAt := n / 2
	wrapAt := n / 3
	switch opt.WrapBits {
	case 0:
		opt.WrapBits = 16
	}
	period := uint32(1) << opt.WrapBits
	half := period / 2

	for i := 0; i < n; i++ {
		base := uint32(i) * frameTick
		switch {
		case opt.Injections[InjectWrap] && i >= wrapAt && i < switchAt:
			// Drive the clock into the upper half then wrap to the low half.
			ts[i] = half + uint32(i-wrapAt)*frameTick
			if ts[i] >= period {
				ts[i] -= period
			}
		case opt.Injections[InjectSwitch] && i >= switchAt:
			// New source restarts its RTP clock near zero.
			ts[i] = uint32(i-switchAt) * frameTick
		default:
			ts[i] = base
		}
	}
	return ts
}

func writeRTP(path string, units []genUnit, timestamps []uint32) error {
	var out []byte
	for i := range units {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i + 1),
				Timestamp:      timestamps[i],
				SSRC:           0x11223344,
			},
			Payload: units[i].data,
		}
		b, err := pkt.Marshal()
		if err != nil {
			return err
		}
		hdr := make([]byte, 2)
		binary.LittleEndian.PutUint16(hdr, uint16(len(b)))
		out = append(out, hdr...)
		out = append(out, b...)
	}
	return os.WriteFile(path, out, 0o600)
}

func writeTruncated(stem string, good []byte) error {
	// Drop the final byte so the last declared unit runs past the new EOF.
	bad := good[:len(good)-1]
	if err := os.WriteFile(stem+".truncated.bits", bad, 0o600); err != nil {
		return err
	}
	// Index still references the original, now out-of-range extent.
	idx := stream.Index{
		Codec: stream.CodecH264,
		Entries: []stream.IndexEntry{{
			Offset: int64(len(good) - 4), Length: 4,
		}},
	}
	b, err := json.MarshalIndent(&idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(stem+".truncated.idx.json", b, 0o600)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
