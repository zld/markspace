// Package fixture generates deterministic local sample media so every demo
// and test runs without external files. All payload bytes are derived from a
// seed; identical seeds always produce identical streams.
package fixture

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"strings"
)

// SegmentSpec describes one synthetic fragmented-MP4 segment.
type SegmentSpec struct {
	Samples       int   // sample count
	SampleTicks   int   // per-sample duration at 90kHz
	TargetBytes   int   // approximate total segment size
	KeyframeEvery int   // sync sample interval in samples
	Seed          int64 // payload seed
}

// BuildSegment renders a structurally valid fMP4 segment whose total size is
// close to TargetBytes. It builds twice: once to measure the exact container
// overhead, then with the payload adjusted to hit the requested size.
func BuildSegment(spec SegmentSpec) []byte {
	if spec.Samples <= 0 {
		spec.Samples = 150
	}
	if spec.SampleTicks <= 0 {
		spec.SampleTicks = 3603
	}
	if spec.KeyframeEvery <= 0 {
		spec.KeyframeEvery = 25
	}
	overhead := len(buildSegment(spec, 1, 0))
	payload := spec.TargetBytes - overhead + spec.Samples
	if payload < spec.Samples {
		payload = spec.Samples
	}
	per := payload / spec.Samples
	if per < 1 {
		per = 1
	}
	return buildSegment(spec, per, spec.Seed)
}

// buildSegment emits the boxes in file order: styp, moov(trak(mdia(minf(stbl)))
// stbl(stts,stsz,stss)), mdat.
func buildSegment(spec SegmentSpec, perSample int, seed int64) []byte {
	rng := rand.New(rand.NewSource(seed))
	mdhd := box("mdhd", full(0, 0,
		u32(0), u32(0),
		u32(90000),
		u32(uint32(spec.Samples*spec.SampleTicks)),
		[]byte{0x55, 0xc4, 0x00, 0x00},
	))
	tkhd := box("tkhd", full(0, 7,
		u32(0), u32(0), u32(1),
		u32(0), u32(uint32(spec.Samples*spec.SampleTicks)),
		u32(0), u32(0), u32(0), u32(0),
		make([]byte, 36),
		u32(1280<<16), u32(720<<16),
	))
	hdlr := box("hdlr", full(0, 0, u32(0), []byte("vide"), u32(0), u32(0), u32(0), []byte{0}))
	var sttsEntries []byte
	for i := 0; i < spec.Samples; i++ {
		sttsEntries = append(sttsEntries, concat(u32(1), u32(uint32(spec.SampleTicks)))...)
	}
	stts := box("stts", full(0, 0, u32(uint32(spec.Samples)), sttsEntries))
	var sizes []byte
	for i := 0; i < spec.Samples; i++ {
		sizes = append(sizes, u32(uint32(perSample))...)
	}
	stsz := box("stsz", full(0, 0, u32(0), u32(uint32(spec.Samples)), sizes))
	var sync []byte
	var count uint32
	for i := 1; i <= spec.Samples; i += spec.KeyframeEvery {
		sync = append(sync, u32(uint32(i))...)
		count++
	}
	stss := box("stss", full(0, 0, u32(count), sync))
	body := make([]byte, spec.Samples*perSample)
	for i := range body {
		body[i] = byte(rng.Intn(256))
	}
	mdat := box("mdat", body)
	moov := box("moov", box("trak", tkhd, box("mdia", mdhd, hdlr, box("minf", box("stbl", stts, stsz, stss)))))
	return concat(box("styp", []byte("msdh"), u32(0), []byte("msdh")), moov, mdat)
}

// HLSManifest renders a media playlist. Durations are formatted with exactly
// three decimals so the rational timeline is predictable.
func HLSManifest(targetSeconds string, durationSeconds []string, ids []string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%s\n", targetSeconds)
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	for i, d := range durationSeconds {
		fmt.Fprintf(&b, "#EXTINF:%s,\n%s\n", d, ids[i])
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// DASHManifest renders an MPD with a fixed-duration segment template.
func DASHManifest(presentationSeconds string, timescale, durationTicks, bandwidth int64) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MPD type="static" mediaPresentationDuration="%s">
  <Period id="p0" start="PT0S">
    <AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1.640028">
      <SegmentTemplate timescale="%d" duration="%d" startNumber="0"
        initialization="init.mp4" media="seg-$Number$.m4s"/>
      <Representation id="v1" bandwidth="%d" codecs="avc1.640028"/>
    </AdaptationSet>
  </Period>
</MPD>
`, presentationSeconds, timescale, durationTicks, bandwidth)
}

// Truncate cuts a byte slice to keep bytes.
func Truncate(b []byte, keep int) []byte {
	if keep >= len(b) {
		return append([]byte(nil), b...)
	}
	return append([]byte(nil), b[:keep]...)
}

// CorruptBoxLength overwrites the 32-bit size of the box at offset at with a
// value far past the end of the segment.
func CorruptBoxLength(b []byte, at int) []byte {
	out := append([]byte(nil), b...)
	if at < 0 || at+4 > len(out) {
		return out
	}
	binary.BigEndian.PutUint32(out[at:at+4], uint32(len(out))+65536)
	return out
}

func box(typ string, chunks ...[]byte) []byte {
	body := concat(chunks...)
	out := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(out, uint32(8+len(body)))
	copy(out[4:8], typ)
	return append(out, body...)
}

func concat(chunks ...[]byte) []byte {
	var out []byte
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func full(version byte, flags uint32, chunks ...[]byte) []byte {
	b := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return append(b, concat(chunks...)...)
}
