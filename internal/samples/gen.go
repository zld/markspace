// Package samples is the deterministic fixture generator. It builds
// normal media plus the fault inputs (unknown-size truncation, cluster
// boundary truncation, timestamp wrap, multi-track disorder, index
// digest mismatch) entirely in code; no external media is referenced.
package samples

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"mediaqc/internal/ebml"
)

// Block is one generated SimpleBlock.
type Block struct {
	Track    uint64
	TC       int16 // relative to cluster timestamp, in ticks (ms)
	Keyframe bool
	Payload  []byte
}

// Cluster is one generated cluster.
type Cluster struct {
	TS          uint64 // cluster timestamp in ticks
	UnknownSize bool
	Blocks      []Block
}

// Track describes one generated track.
type Track struct {
	Number uint64
	Type   uint64 // 1 video, 2 audio
	Codec  string
}

// Spec fully describes a generated file.
type Spec struct {
	Scale          uint64
	Tracks         []Track
	Clusters       []Cluster
	SegmentUnknown bool
	Truncate       int // if >0, the file is cut to this many bytes
}

// payload derives deterministic pseudo-content for block n.
func payload(n, size int) []byte {
	sum := sha256.Sum256([]byte(fmt.Sprintf("mediaqc-payload-%d", n)))
	out := make([]byte, size)
	for i := range out {
		out[i] = sum[i%len(sum)]
	}
	return out
}

// --- EBML encoding helpers (encoder side of internal/ebml) ---

func encID(buf *bytes.Buffer, id uint32) {
	switch {
	case id > 0xFFFFFF:
		buf.WriteByte(byte(id >> 24))
		fallthrough
	case id > 0xFFFF:
		buf.WriteByte(byte(id >> 16))
		fallthrough
	case id > 0xFF:
		buf.WriteByte(byte(id >> 8))
	}
	buf.WriteByte(byte(id))
}

func encSize(buf *bytes.Buffer, size int) {
	switch {
	case size < 0x7F:
		buf.WriteByte(0x80 | byte(size))
	case size < 0x3FFF:
		buf.WriteByte(0x40 | byte(size>>8))
		buf.WriteByte(byte(size))
	default:
		buf.WriteByte(0x20 | byte(size>>24))
		buf.WriteByte(byte(size >> 16))
		buf.WriteByte(byte(size >> 8))
		buf.WriteByte(byte(size))
	}
}

func elem(id uint32, payload []byte) []byte {
	var b bytes.Buffer
	encID(&b, id)
	encSize(&b, len(payload))
	b.Write(payload)
	return b.Bytes()
}

func elemUnknown(id uint32, payload []byte) []byte {
	var b bytes.Buffer
	encID(&b, id)
	b.Write([]byte{0x1F, 0xFF, 0xFF, 0xFF}) // 4-byte unknown size
	b.Write(payload)
	return b.Bytes()
}

func uintElem(id uint32, v uint64) []byte {
	var p bytes.Buffer
	switch {
	case v > 0xFFFFFF:
		p.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
	case v > 0xFFFF:
		p.Write([]byte{byte(v >> 16), byte(v >> 8), byte(v)})
	case v > 0xFF:
		p.Write([]byte{byte(v >> 8), byte(v)})
	default:
		p.WriteByte(byte(v))
	}
	return elem(id, p.Bytes())
}

func simpleBlock(b Block) []byte {
	var p bytes.Buffer
	p.WriteByte(0x80 | byte(b.Track)) // track numbers < 128
	var tc [2]byte
	binary.BigEndian.PutUint16(tc[:], uint16(b.TC))
	p.Write(tc[:])
	if b.Keyframe {
		p.WriteByte(0x80)
	} else {
		p.WriteByte(0x00)
	}
	p.Write(b.Payload)
	return elem(ebml.IDSimpleBlock, p.Bytes())
}

// Build renders a Spec into EBML bytes.
func Build(s Spec) []byte {
	var seg bytes.Buffer
	seg.Write(elem(ebml.IDInfo, uintElem(ebml.IDTimestampScale, s.Scale)))
	var tracks bytes.Buffer
	for _, t := range s.Tracks {
		var e bytes.Buffer
		e.Write(uintElem(ebml.IDTrackNumber, t.Number))
		e.Write(uintElem(ebml.IDTrackType, t.Type))
		e.Write(elem(ebml.IDCodecID, []byte(t.Codec)))
		tracks.Write(elem(ebml.IDTrackEntry, e.Bytes()))
	}
	seg.Write(elem(ebml.IDTracks, tracks.Bytes()))
	n := 0
	for _, c := range s.Clusters {
		var cl bytes.Buffer
		cl.Write(uintElem(ebml.IDTimestamp, c.TS))
		for _, b := range c.Blocks {
			n++
			if b.Payload == nil {
				b.Payload = payload(n, 96+n%53)
			}
			cl.Write(simpleBlock(b))
		}
		if c.UnknownSize {
			seg.Write(elemUnknown(ebml.IDCluster, cl.Bytes()))
		} else {
			seg.Write(elem(ebml.IDCluster, cl.Bytes()))
		}
	}
	var out bytes.Buffer
	out.Write(elem(ebml.IDEBML, []byte{0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'}))
	if s.SegmentUnknown {
		out.Write(elemUnknown(ebml.IDSegment, seg.Bytes()))
	} else {
		out.Write(elem(ebml.IDSegment, seg.Bytes()))
	}
	data := out.Bytes()
	if s.Truncate > 0 && s.Truncate < len(data) {
		data = data[:s.Truncate]
	}
	return data
}

func avTracks() []Track {
	return []Track{{1, 1, "V_VP9"}, {2, 2, "A_OPUS"}}
}

// avBlocks emits one cluster's interleaved A/V blocks on a 33ms video
// grid; audio runs 1ms ahead of video, well inside the sync tolerance.
func avBlocks(clusterTS uint64, keyframe bool) []Block {
	return []Block{
		{Track: 2, TC: 1},
		{Track: 1, TC: 0, Keyframe: keyframe},
		{Track: 2, TC: 34},
		{Track: 1, TC: 33},
		{Track: 2, TC: 67},
		{Track: 1, TC: 66},
	}
}

// Normal is a well-formed two-track file: unknown-size segment and
// clusters, interleaved blocks, 1ms A/V offset.
func Normal() Spec {
	var clusters []Cluster
	for i := 0; i < 6; i++ {
		clusters = append(clusters, Cluster{
			TS:          uint64(i * 100),
			UnknownSize: true,
			Blocks:      avBlocks(uint64(i*100), i%2 == 0),
		})
	}
	return Spec{Scale: 1_000_000, Tracks: avTracks(), Clusters: clusters, SegmentUnknown: true}
}

// Wrap restarts cluster timestamps mid-file (muxer restart): the
// backward jump exceeds the wrap threshold and must be repaired.
func Wrap() Spec {
	ts := []uint64{0, 2000, 4000, 6000, 50, 2050}
	var clusters []Cluster
	for i, t := range ts {
		clusters = append(clusters, Cluster{
			TS:          t,
			UnknownSize: true,
			Blocks:      avBlocks(t, i == 0 || i == 3),
		})
	}
	return Spec{Scale: 1_000_000, Tracks: avTracks(), Clusters: clusters, SegmentUnknown: true}
}

// Disorder swaps two audio blocks inside one cluster, producing a small
// local timestamp regression per track.
func Disorder() Spec {
	s := Normal()
	c := &s.Clusters[2]
	c.Blocks[2], c.Blocks[0] = c.Blocks[0], c.Blocks[2] // A@34 before A@1
	return s
}

// TruncUnknown cuts the file inside a block payload of an unknown-size
// cluster inside an unknown-size segment.
func TruncUnknown() Spec {
	s := Normal()
	full := Build(s)
	s.Truncate = len(full) - 13
	return s
}

// TruncCluster uses a declared cluster size and cuts the file before
// the cluster boundary.
func TruncCluster() Spec {
	s := Normal()
	for i := range s.Clusters {
		s.Clusters[i].UnknownSize = false
	}
	s.SegmentUnknown = false
	full := Build(s)
	s.Truncate = len(full) - 40
	return s
}

// Names lists every generated sample file.
func Names() []string {
	return []string{
		"normal.webm",
		"corrupt-trunc-unknown.webm",
		"corrupt-trunc-cluster.webm",
		"corrupt-wrap.webm",
		"corrupt-disorder.webm",
		"corrupt-mismatch.webm",
	}
}

// WriteAll generates every sample into dir. The mismatch sample is a
// valid file shipped with a stale index sidecar bound to a different
// input digest, exercising the discard-and-rescan path.
func WriteAll(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := map[string][]byte{
		"normal.webm":                 Build(Normal()),
		"corrupt-trunc-unknown.webm":  Build(TruncUnknown()),
		"corrupt-trunc-cluster.webm":  Build(TruncCluster()),
		"corrupt-wrap.webm":           Build(Wrap()),
		"corrupt-disorder.webm":       Build(Disorder()),
		"corrupt-mismatch.webm":       Build(Normal()),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	// Stale sidecar: a well-formed index bound to a foreign digest.
	mismatch := files["corrupt-mismatch.webm"]
	res, err := ebml.Scan(bytes.NewReader(mismatch), int64(len(mismatch)))
	if err != nil {
		return err
	}
	foreign := sha256.Sum256([]byte("some other master file"))
	idx := ebml.Build(res, foreign)
	return ebml.SaveIndex(filepath.Join(dir, "corrupt-mismatch.webm.qcindex"), idx)
}
