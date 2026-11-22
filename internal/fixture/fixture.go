package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"

	"github.com/pion/rtp"

	"mediad/internal/core"
)

// Kind classifies a generated asset.
type Kind string

const (
	KindSRT        Kind = "srt"
	KindVTT        Kind = "vtt"
	KindMP4        Kind = "mp4"
	KindCorruptMP4 Kind = "corrupt-mp4"
	KindRTP        Kind = "rtp"
	KindTimecode   Kind = "timecode"
	KindDrift      Kind = "drift"
)

// File is one generated in-memory asset.
type File struct {
	Name string
	Kind Kind
	Data []byte
}

// Stream is the complete demo input set.
type Stream struct {
	Files         []File
	NormalFrames  int
	CorruptCues   int
	KeyframeGaps  int
	ExpectedGaps  []int64 // keyframe gaps in track ticks, in order
	Expected      []Expectation
	CorruptOffset []int64 // offsets where damage was injected, ascending
}

// Expectation is ground truth used by the demo to measure offset error.
type Expectation struct {
	Asset    string
	Format   string
	Category string
	Span     core.Span
}

// Options tunes generation. Zero values fall back to the demo defaults.
type Options struct {
	Seed        int64
	VideoFrames int
	SRTCues     int
	VTTCues     int
	RTPPackets  int
}

// DemoDefaults are used by the CLI demo command.
func DemoDefaults() Options {
	return Options{Seed: 42, VideoFrames: 150, SRTCues: 12, VTTCues: 12, RTPPackets: 60}
}

// Generate builds a deterministic fixture set. The same Options always
// produce byte-identical assets, which is what makes the report checksum
// reproducible.
func Generate(opt Options) (*Stream, error) {
	if opt.VideoFrames <= 0 {
		opt = DemoDefaults()
	}
	if opt.VideoFrames < 120 || opt.SRTCues+opt.VTTCues < 20 {
		return nil, fmt.Errorf("fixture: need at least 120 frames and 20 cues, got %d/%d",
			opt.VideoFrames, opt.SRTCues+opt.VTTCues)
	}
	rng := rand.New(rand.NewSource(opt.Seed))
	st := &Stream{}

	// Normal video: VFR deltas, GOP of 30, B-frame composition offsets.
	deltas := make([]uint32, opt.VideoFrames)
	syncs := make([]int, 0, opt.VideoFrames/30+1)
	for i := range deltas {
		deltas[i] = 1001 // 29.97 fps nominal at timescale 30000
		if i%11 == 5 {
			deltas[i] = 3003 // a VFR long frame
		}
	}
	for i := 1; i <= opt.VideoFrames; i += 30 {
		syncs = append(syncs, i)
	}
	ctts := make([]int64, opt.VideoFrames)
	for i := range ctts {
		switch i % 3 {
		case 1:
			ctts[i] = 3003
		case 2:
			ctts[i] = 1001
		}
	}
	normal := buildMP4(mp4Spec{
		Timescale:      30000,
		MovieTimescale: 1000,
		FrameDeltas:    deltas,
		CTTSOffsets:    ctts,
		SyncFrames:     syncs,
	})
	st.Files = append(st.Files, File{Name: "video/normal.mp4", Kind: KindMP4, Data: normal})
	st.NormalFrames = opt.VideoFrames
	gaps := make([]int64, 0, len(syncs)-1)
	for i := 1; i < len(syncs); i++ {
		g := int64(0)
		for k := syncs[i-1]; k < syncs[i]; k++ {
			g += int64(deltas[k-1])
		}
		gaps = append(gaps, g)
	}
	st.ExpectedGaps = gaps
	st.KeyframeGaps = len(gaps)

	// Corrupt video: a good moov, an mdat whose declared size overruns the
	// file, and a second truncated mdat after it.
	trunc, truncAt, truncDecl := corruptMP4(normal)
	st.Files = append(st.Files, File{Name: "video/corrupt.mp4", Kind: KindCorruptMP4, Data: trunc})
	st.Expected = append(st.Expected, Expectation{
		Asset: "video/corrupt.mp4", Format: "mp4", Category: "truncated_box",
		Span: core.Span{Start: truncAt, End: int64(len(trunc))},
	})
	st.CorruptOffset = append(st.CorruptOffset, truncAt)

	lengthCorrupt, lengthAt := lengthMP4(normal)
	st.Files = append(st.Files, File{Name: "video/length.mp4", Kind: KindCorruptMP4, Data: lengthCorrupt})
	st.Expected = append(st.Expected, Expectation{
		Asset: "video/length.mp4", Format: "mp4", Category: "mp4_length",
		Span: core.Span{Start: lengthAt, End: lengthAt + 8},
	})
	st.CorruptOffset = append(st.CorruptOffset, lengthAt)

	st.Files = append(st.Files, File{
		Name: "subs/normal.srt", Kind: KindSRT,
		Data: buildSRT(opt.SRTCues, false, rng),
	})
	corruptSRT := buildSRT(opt.SRTCues/2+1, true, rng)
	st.Files = append(st.Files, File{Name: "subs/corrupt.srt", Kind: KindSRT, Data: corruptSRT})
	if off := bytes.Index(corruptSRT, []byte("00:00:99,999")); off >= 0 {
		st.Expected = append(st.Expected, Expectation{
			Asset: "subs/corrupt.srt", Format: "srt", Category: "srt_time",
			Span: core.Span{Start: int64(off), End: int64(off) + 12},
		})
		st.CorruptOffset = append(st.CorruptOffset, int64(off))
	}
	if off := bytes.Index(corruptSRT, []byte("truncated tail")); off >= 0 {
		st.Expected = append(st.Expected, Expectation{
			Asset: "subs/corrupt.srt", Format: "srt", Category: "truncated_cue",
			Span: core.Span{Start: int64(off), End: int64(len(corruptSRT))},
		})
	}
	st.CorruptCues = 2

	st.Files = append(st.Files, File{
		Name: "subs/normal.vtt", Kind: KindVTT,
		Data: buildVTT(opt.VTTCues, false),
	})
	brokenVTT := buildVTT(opt.VTTCues/2+1, true)
	st.Files = append(st.Files, File{Name: "subs/broken.vtt", Kind: KindVTT, Data: brokenVTT})
	if off := bytes.Index(brokenVTT, []byte("00:09.500")); off >= 0 {
		st.Expected = append(st.Expected, Expectation{
			Asset: "subs/broken.vtt", Format: "vtt", Category: "vtt_time",
			Span: core.Span{Start: int64(off), End: int64(off) + 9},
		})
		st.CorruptOffset = append(st.CorruptOffset, int64(off))
	}

	gapped := buildRTP(16, true)
	st.Files = append(st.Files,
		File{Name: "net/stream.rtp", Kind: KindRTP, Data: buildRTP(opt.RTPPackets, false)},
		File{Name: "net/gapped.rtp", Kind: KindRTP, Data: gapped},
	)
	// The gap is observed on the packet that follows the injected loss: its
	// sequence number jumps past the three that were dropped.
	if off := rtpRecordOffset(gapped, 6); off >= 0 {
		st.Expected = append(st.Expected, Expectation{
			Asset: "net/gapped.rtp", Format: "rtp", Category: "rtp_sequence_gap",
			Span: core.Span{Start: off, End: off + 20},
		})
		st.CorruptOffset = append(st.CorruptOffset, off)
	}

	driftData := buildDrift()
	st.Files = append(st.Files, File{Name: "net/edge-b.drift", Kind: KindDrift, Data: driftData})
	// Drift is a fit over the sample list; the span runs from the first sample
	// line through the last one, so the first sample is the anchor.
	if off := bytes.Index(driftData, []byte("0,5\n")); off >= 0 {
		st.Expected = append(st.Expected, Expectation{
			Asset: "net/edge-b.drift", Format: "drift", Category: "node_drift",
			Span: core.Span{Start: int64(off), End: int64(off) + 4},
		})
		st.CorruptOffset = append(st.CorruptOffset, int64(off))
	}

	tcData := buildTimecodes()
	st.Files = append(st.Files, File{Name: "tc/labels.tc", Kind: KindTimecode, Data: tcData})
	if off := bytes.Index(tcData, []byte("00:01:00;00")); off >= 0 {
		st.Expected = append(st.Expected, Expectation{
			Asset: "tc/labels.tc", Format: "timecode", Category: "timecode_label",
			Span: core.Span{Start: int64(off), End: int64(off) + 11},
		})
		st.CorruptOffset = append(st.CorruptOffset, int64(off))
	}
	_ = truncDecl
	return st, nil
}

// rtpRecordOffset returns the byte offset of the nth length-prefixed packet.
func rtpRecordOffset(data []byte, n int) int64 {
	off := int64(0)
	for i := 0; i < n; i++ {
		if off+4 > int64(len(data)) {
			return -1
		}
		size := int64(binary.BigEndian.Uint32(data[off:]))
		off += 4 + size
	}
	if off >= int64(len(data)) {
		return -1
	}
	return off
}

func buildDrift() []byte {
	// Node edge-b runs 1500 ppm fast and adds a 5 ms offset. Both the slope
	// and the intercept are recoverable exactly from these samples.
	var b bytes.Buffer
	b.WriteString("#node edge-b\n")
	b.WriteString("# source_ms,node_ms\n")
	for i := int64(0); i <= 20; i++ {
		src := i * 1000
		node := src + (src*3)/2000 + 5
		fmt.Fprintf(&b, "%d,%d\n", src, node)
	}
	return b.Bytes()
}

// corruptMP4 physically truncates the media data while leaving the corpus
// header's declared size intact, so the mdat is a truncated box and samples
// past the cut cannot be trusted. It returns the data, the mdat offset and the
// declared size.
func corruptMP4(orig []byte) ([]byte, int64, uint32) {
	off := topLevelMdat(orig)
	if off < 0 {
		return orig, 0, 0
	}
	declared := binary.BigEndian.Uint32(orig[off:])
	cut := off + 8 + (len(orig)-off-8)/3
	out := make([]byte, cut)
	copy(out, orig[:cut])
	return out, int64(off), declared
}

// lengthMP4 inserts a top-level box whose declared size is smaller than its
// own header, which is an unambiguous length-field fault. The scanner must
// resynchronize and still find the following moov and mdat.
func lengthMP4(orig []byte) ([]byte, int64) {
	// Insert the bad box after the complete ftyp so the fault is a genuine
	// sibling-level length error rather than damage inside a box payload.
	first := 0
	if len(orig) >= 8 {
		first = int(binary.BigEndian.Uint32(orig))
	}
	if first < 8 || first > len(orig) {
		first = 8
	}
	out := make([]byte, 0, len(orig)+8)
	out = append(out, orig[:first]...)
	at := int64(len(out))
	out = append(out, be32(2)...)
	out = append(out, []byte("free")...)
	out = append(out, orig[first:]...)
	return out, at
}

func topLevelMdat(data []byte) int {
	off := 0
	for off+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[off:]))
		typ := string(data[off+4 : off+8])
		if typ == "mdat" {
			return off
		}
		if size < 8 {
			return -1
		}
		off += size
	}
	return -1
}

func srtStamp(ms int64) string {
	h := ms / 3600000
	ms -= h * 3600000
	m := ms / 60000
	ms -= m * 60000
	s := ms / 1000
	ms -= s * 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

func buildSRT(n int, corrupt bool, rng *rand.Rand) []byte {
	var b []byte
	for i := 1; i <= n; i++ {
		start := int64(i-1)*2000 + int64(rng.Intn(120))
		end := start + 1500
		b = append(b, []byte(fmt.Sprintf("%d\n%s --> %s\n", i, srtStamp(start), srtStamp(end)))...)
		if i%4 == 0 {
			b = append(b, []byte("two line\nsubtitle body\n\n")...)
		} else {
			b = append(b, []byte(fmt.Sprintf("edge cue %d\n\n", i))...)
		}
		if corrupt && i == 2 {
			// Second cue with a nonsense seconds field.
			b = append(b, []byte("00:00:99,999 --> 00:00:02,000\nbad cue\n\n")...)
		}
	}
	if corrupt {
		// Final cue whose body is truncated without a newline.
		b = append(b, []byte(fmt.Sprintf("%d\n%s --> %s\ntruncated tail", n+2, srtStamp(int64(n)*2200), srtStamp(int64(n)*2200+1000)))...)
	}
	return b
}

func vttStamp(ms int64) string {
	h := ms / 3600000
	ms -= h * 3600000
	m := ms / 60000
	ms -= m * 60000
	s := ms / 1000
	ms -= s * 1000
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
	}
	return fmt.Sprintf("%02d:%02d.%03d", m, s, ms)
}

func buildVTT(n int, broken bool) []byte {
	var b []byte
	b = append(b, []byte("WEBVTT - mediad fixture\n\n")...)
	b = append(b, []byte("NOTE generated by fixture, deterministic\n\n")...)
	for i := 1; i <= n; i++ {
		start := int64(i-1) * 1800
		end := start + 1200
		b = append(b, []byte(fmt.Sprintf("cue-%d\n%s --> %s align:start position:%d%%\n", i, vttStamp(start), vttStamp(end), 5+i*3))...)
		if i%3 == 0 {
			b = append(b, []byte("line one\nline two\n\n")...)
		} else {
			b = append(b, []byte(fmt.Sprintf("vtt cue %d\n\n", i))...)
		}
		if broken && i == 2 {
			b = append(b, []byte("00:09.500 --> 00:07.000\nbackwards\n\n")...)
		}
	}
	return b
}

func buildRTP(n int, gapped bool) []byte {
	var out []byte
	seq := uint16(65530)
	ts := uint32(0xFFFFF000)
	for i := 0; i < n; i++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				Marker:         i%15 == 14,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           0x4d454449,
			},
			Payload: []byte{byte(i), byte(i >> 8), 0x65, 0x88},
		}
		raw, _ := pkt.Marshal()
		var n32 [4]byte
		binary.BigEndian.PutUint32(n32[:], uint32(len(raw)))
		out = append(out, n32[:]...)
		out = append(out, raw...)
		seq++
		if gapped && i == 5 {
			seq += 3 // lose three packets
		}
		ts += 3003
	}
	return out
}

func buildTimecodes() []byte {
	lines := []string{
		"00:00:00:00",
		"00:00:01:00",
		"00:01:00;02",
		"00:01:00;00",
		"00:09:59;29",
		"00:10:00;00",
		"01:00:00:00",
	}
	out := []byte{}
	for _, l := range lines {
		out = append(out, []byte(l+"\n")...)
	}
	return out
}

// WriteAll writes every generated file into dir using its relative name.
func (s *Stream) WriteAll(dir string, write func(name string, data []byte) error) error {
	for _, f := range s.Files {
		if err := write(f.Name, f.Data); err != nil {
			return fmt.Errorf("fixture: write %s: %w", f.Name, err)
		}
	}
	return nil
}

// Names returns the file names in generation order.
func (s *Stream) Names() []string {
	out := make([]string, 0, len(s.Files))
	for _, f := range s.Files {
		out = append(out, f.Name)
	}
	return out
}
