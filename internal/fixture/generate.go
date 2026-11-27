// Package fixture deterministically generates all sample media used by
// tests and acceptance: a valid multi-track Matroska file, corrupt
// variants, RTP capture events, task-key lists, and an MP4 timebase
// cross-check file. No external assets are referenced.
package fixture

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"
	"github.com/pion/rtp"

	"mediad/internal/merge"
)

// Names of generated files within the fixture directory.
const (
	ValidMKV       = "valid.mkv"
	TruncatedMKV   = "corrupt-truncated.mkv"
	InvalidVINTMKV = "corrupt-vint.mkv"
	OutOfBoundsMKV = "corrupt-oob.mkv"
	RTPEvents      = "events.rtp"
	TasksOK        = "tasks-ok.json"
	TasksConflict  = "tasks-conflict.json"
	MP4Check       = "check.mp4"
)

// TimecodeScaleNs is the Matroska timecode scale used by all fixtures (1ms).
const TimecodeScaleNs = 1_000_000

// MediaDurationNs is the total duration of the valid fixture.
const MediaDurationNs = 240_000_000

// Set lists the generated file paths.
type Set struct {
	Dir string
}

// Path returns the full path of a named fixture file.
func (s *Set) Path(name string) string { return filepath.Join(s.Dir, name) }

// Generate writes all fixture files into dir (created if needed).
func Generate(dir string) (*Set, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Set{Dir: dir}
	writes := []struct {
		name string
		data []byte
	}{
		{ValidMKV, validMKV()},
		{TruncatedMKV, truncatedMKV()},
		{InvalidVINTMKV, invalidVINTMKV()},
		{OutOfBoundsMKV, outOfBoundsMKV()},
		{RTPEvents, rtpEvents()},
	}
	for _, w := range writes {
		if err := os.WriteFile(s.Path(w.name), w.data, 0o644); err != nil {
			return nil, err
		}
	}
	if err := writeTasks(s); err != nil {
		return nil, err
	}
	if err := writeMP4Check(s.Path(MP4Check)); err != nil {
		return nil, err
	}
	return s, nil
}

// --- EBML writer helpers ---

type ebmlWriter struct{ buf []byte }

func (w *ebmlWriter) id(id uint32) {
	switch {
	case id > 0xFFFFFF:
		w.buf = append(w.buf, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFFFF:
		w.buf = append(w.buf, byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFF:
		w.buf = append(w.buf, byte(id>>8), byte(id))
	default:
		w.buf = append(w.buf, byte(id))
	}
}

// size writes a minimal-length size VINT (payloads stay below 2MiB).
func (w *ebmlWriter) size(n int) {
	switch {
	case n < 127:
		w.buf = append(w.buf, 0x80|byte(n))
	case n < 16382:
		w.buf = append(w.buf, 0x40|byte(n>>8), byte(n))
	default:
		w.buf = append(w.buf, 0x20|byte(n>>16), byte(n>>8), byte(n))
	}
}

func (w *ebmlWriter) unknown() { w.buf = append(w.buf, 0xFF) }

func (w *ebmlWriter) elem(id uint32, payload []byte) {
	w.id(id)
	w.size(len(payload))
	w.buf = append(w.buf, payload...)
}

func (w *ebmlWriter) uintElem(id uint32, v uint64) {
	var b [8]byte
	n := 8
	for v > 0 {
		n--
		b[n] = byte(v)
		v >>= 8
	}
	if n == 8 {
		n = 7
	}
	w.elem(id, b[n:])
}

// framePayload is the deterministic content of one frame.
func framePayload(track uint64, tick int64) []byte {
	sum := sha256.Sum256([]byte(fmt.Sprintf("frame:%d:%d", track, tick)))
	return sum[:24]
}

// simpleBlock builds a non-laced SimpleBlock payload.
func simpleBlock(track uint64, tc int16, keyframe bool) []byte {
	p := []byte{0x80 | byte(track), byte(uint16(tc) >> 8), byte(uint16(tc))}
	if keyframe {
		p = append(p, 0x80)
	} else {
		p = append(p, 0x00)
	}
	return append(p, framePayload(track, int64(tc))...)
}

const (
	idEBML          = 0x1A45DFA3
	idSegment       = 0x18538067
	idInfo          = 0x1549A966
	idTimecodeScale = 0x2AD7B1
	idTracks        = 0x1654AE6B
	idTrackEntry    = 0xAE
	idTrackNumber   = 0xD7
	idCluster       = 0x1F43B675
	idTimecode      = 0xE7
	idSimpleBlock   = 0xA3
)

// cluster builds one cluster's payload: timecode base plus interleaved
// blocks for video (track 1, keyframe at base) and audio (track 2).
func cluster(base int64, unknownSize bool, w *ebmlWriter) {
	var c ebmlWriter
	c.uintElem(idTimecode, uint64(base))
	c.elem(idSimpleBlock, simpleBlock(1, 0, true))
	c.elem(idSimpleBlock, simpleBlock(2, 0, false))
	c.elem(idSimpleBlock, simpleBlock(1, 40, false))
	c.elem(idSimpleBlock, simpleBlock(2, 40, false))
	w.id(idCluster)
	if unknownSize {
		w.unknown()
	} else {
		w.size(len(c.buf))
	}
	w.buf = append(w.buf, c.buf...)
}

// segmentBody is the shared Segment payload of the valid file.
func segmentBody() []byte {
	var seg ebmlWriter
	var info ebmlWriter
	info.uintElem(idTimecodeScale, TimecodeScaleNs)
	seg.elem(idInfo, info.buf)
	var tracks ebmlWriter
	for _, tn := range []uint64{1, 2} {
		var te ebmlWriter
		te.uintElem(idTrackNumber, tn)
		tracks.elem(idTrackEntry, te.buf)
	}
	seg.elem(idTracks, tracks.buf)
	cluster(0, false, &seg)
	cluster(80, true, &seg) // unknown-size cluster
	cluster(160, false, &seg)
	return seg.buf
}

// validMKV is the well-formed sample: unknown-size Segment, two tracks,
// interleaved blocks, one unknown-size Cluster.
func validMKV() []byte {
	var doc ebmlWriter
	doc.elem(idEBML, []byte{0x42, 0x86, 0x81, 0x01}) // DocTypeVersion=1
	doc.id(idSegment)
	doc.unknown()
	doc.buf = append(doc.buf, segmentBody()...)
	return doc.buf
}

// truncatedMKV ends inside the header of a SimpleBlock within an
// unknown-size Cluster: the element ID is present, the size VINT is cut.
func truncatedMKV() []byte {
	var doc ebmlWriter
	doc.elem(idEBML, []byte{0x42, 0x86, 0x81, 0x01})
	doc.id(idSegment)
	doc.unknown()
	var info ebmlWriter
	info.uintElem(idTimecodeScale, TimecodeScaleNs)
	doc.elem(idInfo, info.buf)
	var tracks ebmlWriter
	var te ebmlWriter
	te.uintElem(idTrackNumber, 1)
	tracks.elem(idTrackEntry, te.buf)
	doc.elem(idTracks, tracks.buf)
	// Unknown-size cluster, truncated mid-header of its second block.
	doc.id(idCluster)
	doc.unknown()
	var clu ebmlWriter
	clu.uintElem(idTimecode, 0)
	clu.elem(idSimpleBlock, simpleBlock(1, 0, true))
	clu.id(idSimpleBlock) // size VINT and payload missing: EOF follows
	doc.buf = append(doc.buf, clu.buf...)
	return doc.buf
}

// invalidVINTMKV places a 0x00 byte where an element ID is expected.
func invalidVINTMKV() []byte {
	var doc ebmlWriter
	doc.elem(idEBML, []byte{0x01})
	doc.id(idSegment)
	doc.unknown()
	var clu ebmlWriter
	clu.uintElem(idTimecode, 0)
	doc.elem(idCluster, append(clu.buf, 0x00, 0x80))
	return doc.buf
}

// outOfBoundsMKV has a SimpleBlock whose declared size exceeds its Cluster.
func outOfBoundsMKV() []byte {
	var doc ebmlWriter
	doc.elem(idEBML, []byte{0x01})
	var seg ebmlWriter
	var clu ebmlWriter
	clu.uintElem(idTimecode, 0)
	clu.id(idSimpleBlock)
	clu.size(500) // only a few bytes actually follow
	clu.buf = append(clu.buf, 0x81, 0x00, 0x00, 0x80)
	seg.elem(idCluster, clu.buf)
	doc.elem(idSegment, seg.buf)
	return doc.buf
}

// rtpEvents builds length-prefixed RTP packets for the video keyframe
// capture events at ticks 0, 80, 160 ms (90 kHz clock).
func rtpEvents() []byte {
	var out []byte
	for i, tickMs := range []int64{0, 80, 160} {
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: uint16(1000 + i),
				Timestamp:      uint32(tickMs * 90),
				SSRC:           0x0DEFACED,
			},
			Payload: framePayload(1, tickMs)[:8],
		}
		b, err := pkt.Marshal()
		if err != nil {
			panic(err)
		}
		var rec [2]byte
		binary.BigEndian.PutUint16(rec[:], uint16(len(b)))
		out = append(out, rec[:]...)
		out = append(out, b...)
	}
	return out
}

// writeTasks writes the duplicate-key merge fixtures.
func writeTasks(s *Set) error {
	mk := func(key string, start, off int64, hash string) merge.Segment {
		return merge.Segment{TaskKey: key, StartNs: start, EndNs: start + 120_000_000,
			SrcOffset: off, SrcLength: 128, SHA256: hash}
	}
	ok := [][]merge.Segment{
		{mk("seg-b", 120_000_000, 500, "h-b"), mk("seg-a", 0, 100, "h-a")},
		{mk("seg-a", 0, 100, "h-a")}, // identical duplicate of seg-a
	}
	conflict := [][]merge.Segment{
		{mk("seg-a", 0, 100, "h-a")},
		{mk("seg-a", 0, 100, "h-DIFFERENT")},
	}
	for name, lists := range map[string][][]merge.Segment{
		TasksOK: ok, TasksConflict: conflict,
	} {
		b, err := json.MarshalIndent(lists, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(s.Path(name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeMP4Check writes a minimal MP4 (ftyp + moov/mvhd) whose timescale
// cross-checks the Matroska timebase: 1000 units/s == 1ms per unit.
func writeMP4Check(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := mp4.NewWriter(f)
	// mp4.Marshal writes only the box payload; StartBox/EndBox frame it.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 1,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	_, err = mp4.Marshal(w, &mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  240,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		NextTrackID: 1,
	}, mp4.Context{})
	if err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	return nil
}
