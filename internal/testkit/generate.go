// Package testkit generates deterministic, self-contained fixtures:
// unfragmented AVC MP4 files with known keyframes and optional timeline
// gaps, a truncated container, and JSONL event feeds carrying real RTP
// packets. No external sample files are referenced.
package testkit

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
)

const (
	// FixtureTimescale matches the acceptance RTP clock so anchor math
	// stays integer-exact.
	FixtureTimescale = 90000
	frameDelta       = 3000 // 30 fps
	frameCount       = 300  // 10 seconds
	keyframeEvery    = 30   // one sync sample per second
	width            = 320
	height           = 240
)

// nalBuilder emits minimal but structurally valid length-prefixed AVC
// samples: SPS/PPS live only in avcC, each sample is an IDR or non-IDR
// slice NAL wrapped with a 4-byte length prefix.
type nalBuilder struct {
	sps []byte
	pps []byte
}

func newNALBuilder() *nalBuilder {
	return &nalBuilder{
		sps: []byte{0x67, 0x42, 0x00, 0x0a, 0xf8, 0x41, 0xa2},
		pps: []byte{0x68, 0xce, 0x38, 0x80},
	}
}

// avcC builds the decoder configuration record byte-for-byte.
func (n *nalBuilder) avcC() []byte {
	buf := make([]byte, 0, 32)
	buf = append(buf, 1, n.sps[1], n.sps[2], n.sps[3], 0xFC|3, 0xE0|1)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(n.sps)))
	buf = append(buf, n.sps...)
	buf = append(buf, 1)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(n.pps)))
	buf = append(buf, n.pps...)
	return buf
}

// sample renders one AVC sample; IDR samples are distinguishable from
// inter samples for later debugging.
func (n *nalBuilder) sample(isIDR bool, frame uint32) []byte {
	nalType := byte(1) // non-IDR slice
	if isIDR {
		nalType = 5
	}
	payload := []byte{nalType, 0x88, 0x80, 0x40}
	payload = binary.BigEndian.AppendUint32(payload, frame)
	out := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(out, uint32(len(payload)))
	copy(out[4:], payload)
	return out
}

// boxWriter wraps an *os.File with the mp4 box writer plus raw helpers.
type boxWriter struct {
	f *os.File
	w *mp4.Writer
}

func createBoxWriter(path string) (*boxWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &boxWriter{f: f, w: mp4.NewWriter(f)}, nil
}

func (b *boxWriter) close() error { return b.f.Close() }

func (b *boxWriter) start(t mp4.BoxType) error {
	_, err := b.w.StartBox(&mp4.BoxInfo{Type: t})
	return err
}

func (b *boxWriter) end() error { _, err := b.w.EndBox(); return err }

func (b *boxWriter) raw(data []byte) error {
	_, err := b.f.Write(data)
	return err
}

// writeFtyp emits the brand box shared by every fixture.
func (b *boxWriter) writeFtyp() error {
	if err := b.start(mp4.BoxTypeFtyp()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(b.f, &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
		},
	}, mp4.Context{}); err != nil {
		return err
	}
	return b.end()
}

// Generate writes all acceptance fixtures into outDir: clean.mp4,
// gap.mp4, truncated.mp4 and the three JSONL event feeds.
func Generate(outDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := generateMP4(mp4Spec{path: filepath.Join(outDir, "clean.mp4")}); err != nil {
		return err
	}
	// The doubled delta lands at frame 120 (t=4s); the gap event at t=4s
	// intersects the resulting anomaly interval.
	if err := generateMP4(mp4Spec{path: filepath.Join(outDir, "gap.mp4"), gapAfter: 120}); err != nil {
		return err
	}
	if err := generateMP4(mp4Spec{path: filepath.Join(outDir, "truncated.mp4"), truncated: true}); err != nil {
		return err
	}
	return generateEventFiles(outDir)
}
