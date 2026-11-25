// Package testdata generates the self-contained Matroska/WebM fixtures the
// acceptance commands use. Nothing is downloaded: every byte is written here,
// and each corrupted fixture records the offset the validator is expected to
// report so the CLI can assert an exact 0-byte difference.

package testdata

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	mp4 "github.com/abema/go-mp4"
	"github.com/orcaman/writerseeker"
)

// Fixture files produced by Generate.
const (
	ValidMKV         = "valid.mkv"
	TruncatedMKV     = "truncated.mkv"
	UnknownSizeMKV   = "unknown-size.mkv"
	IllegalLenMKV    = "illegal-length.mkv"
	TrackConflictMKV = "track-conflict.mkv"
	FragOrderMKV     = "fragment-order.mkv"
	ManifestV1       = "manifest-v1.json"
	ManifestV2       = "manifest-v2.json"
	RTPDump          = "audio.rtp.txt"
	// MP4Sidecar is a go-mp4-encoded index sidecar sharing the delivery time base.
	MP4Sidecar = "index-sidecar.mp4"
)

// Expectation is the structured failure the generator pre-computed for a
// damaged fixture. The acceptance run compares the validator's report against
// it byte for byte.
type Expectation struct {
	File      string `json:"file"`
	Category  string `json:"category"`
	ElementID uint32 `json:"element_id"`
	Offset    int64  `json:"offset"`
	// Stage is the pipeline stage expected to surface the failure.
	Stage string `json:"stage"`
	Note  string `json:"note"`
}

// Result is what Generate wrote.
type Result struct {
	Dir          string
	Expectations []Expectation
	// Offsets records the exact byte position of key structures, so reports can
	// be audited against the generator rather than against the parser.
	Offsets map[string]int64
}

// --- EBML encoding ---------------------------------------------------------

func putID(id uint32) []byte {
	switch {
	case id <= 0xFF:
		return []byte{byte(id)}
	case id <= 0xFFFF:
		return []byte{byte(id >> 8), byte(id)}
	case id <= 0xFFFFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	}
}

// putSize encodes a payload length as an EBML size vint.
func putSize(n int) []byte {
	switch {
	case n < 0x7F:
		return []byte{0x80 | byte(n)}
	case n < 0x3FFF:
		return []byte{0x40 | byte(n>>8), byte(n)}
	case n < 0x1FFFFF:
		return []byte{0x20 | byte(n>>16), byte(n >> 8), byte(n)}
	default:
		return []byte{0x10 | byte(n>>24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}

// putUnknown encodes the eight-byte unknown-size sentinel.
func putUnknown() []byte { return []byte{0xFF} }

// el builds a known-length element.
func el(id uint32, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	out := putID(id)
	out = append(out, putSize(len(body))...)
	return append(out, body...)
}

// unknownMaster opens an unknown-length master.
func unknownMaster(id uint32) []byte {
	out := putID(id)
	return append(out, putUnknown()...)
}

func uintEl(id uint32, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	i := 0
	for i < 7 && b[i] == 0 {
		i++
	}
	return el(id, b[i:])
}

func strEl(id uint32, s string) []byte { return el(id, []byte(s)) }

func float64El(id uint32, v float64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], math.Float64bits(v))
	return el(id, b[:])
}

func simpleBlock(track uint64, rel int16, payload []byte) []byte {
	head := []byte{0x80 | byte(track), byte(rel >> 8), byte(rel)}
	return el(0xA3, append(head, payload...))
}

// Segment children sizes are fixed so offsets are stable across seeds; the seed
// only perturbs payload bytes, never structure.
type trackSpec struct {
	number     uint64
	typ        uint64
	codec      string
	lang       string
	name       string
	audio      *audioSpec
	video      *videoSpec
	channels   uint64
	sampleRate float64
	colour     *colourSpec
}

type audioSpec struct {
	channels uint64
	rate     float64
	bitDepth uint64
}

type videoSpec struct {
	w, h   uint64
	colour *colourSpec
}

type colourSpec struct {
	present   bool
	matrix    uint64
	transfer  uint64
	primaries uint64
	rangeVal  uint64
	bits      uint64
	chromaH   uint64
	chromaV   uint64
}

func (s trackSpec) encode() []byte {
	var parts [][]byte
	parts = append(parts, uintEl(0xD7, s.number))
	parts = append(parts, uintEl(0x73C5, s.number+1000))
	parts = append(parts, uintEl(0x83, s.typ))
	parts = append(parts, uintEl(0xB9, 1))
	parts = append(parts, uintEl(0x88, 1))
	parts = append(parts, strEl(0x86, s.codec))
	if s.lang != "" {
		parts = append(parts, strEl(0x22B59C, s.lang))
	}
	if s.name != "" {
		parts = append(parts, strEl(0x536E, s.name))
	}
	if s.audio != nil {
		var ap [][]byte
		ap = append(ap, float64El(0xB5, s.audio.rate))
		ap = append(ap, uintEl(0x9F, s.audio.channels))
		if s.audio.bitDepth > 0 {
			ap = append(ap, uintEl(0x6264, s.audio.bitDepth))
		}
		parts = append(parts, el(0xE1, ap...))
	}
	if s.video != nil {
		var vp [][]byte
		vp = append(vp, uintEl(0xB0, s.video.w))
		vp = append(vp, uintEl(0xBA, s.video.h))
		if c := s.video.colour; c != nil && c.present {
			vp = append(vp, el(0x55B0,
				uintEl(0x55B1, c.matrix),
				uintEl(0x55B2, c.bits),
				uintEl(0x55B3, c.chromaH),
				uintEl(0x55B4, c.chromaV),
				uintEl(0x55B9, c.rangeVal),
				uintEl(0x55BA, c.transfer),
				uintEl(0x55BB, c.primaries),
			))
		}
		parts = append(parts, el(0xE0, vp...))
	}
	return el(0xAE, parts...)
}

// defaultTracks is the healthy multi-language delivery layout: main video,
// Japanese dialogue, English dialogue, two subtitle tracks.
func defaultTracks() []trackSpec {
	return []trackSpec{
		{number: 1, typ: 1, codec: "V_MPEG4/ISO/AVC", lang: "jpn", name: "main",
			video: &videoSpec{w: 1920, h: 1080, colour: &colourSpec{
				present: true, matrix: 9, transfer: 16, primaries: 9, rangeVal: 1,
				bits: 10, chromaH: 1, chromaV: 1}}},
		{number: 2, typ: 2, codec: "A_OPUS", lang: "jpn", name: "dialogue",
			audio: &audioSpec{channels: 2, rate: 48000, bitDepth: 16}},
		{number: 3, typ: 2, codec: "A_OPUS", lang: "eng", name: "dialogue",
			audio: &audioSpec{channels: 6, rate: 48000, bitDepth: 16}},
		{number: 4, typ: 17, codec: "S_TEXT/UTF8", lang: "eng", name: "dialogue"},
		{number: 5, typ: 17, codec: "S_TEXT/ASS", lang: "jpn", name: "signs"},
	}
}

// segmentLayout describes the exact cluster/block plan so offsets are
// predictable. Each cluster holds one block per track in interleaved order.
type blockPlan struct {
	track    uint64
	relTicks int16
	payload  []byte
}

type clusterPlan struct {
	timecode uint64
	unknown  bool
	blocks   []blockPlan
}

func defaultClusters(seed int64) []clusterPlan {
	base := []byte{0x00, 0x01, 0x02, 0x03}
	var out []clusterPlan
	for c := 0; c < 2; c++ {
		tc := uint64(c) * 1000
		p := clusterPlan{timecode: tc}
		for _, tr := range []struct{ n uint64 }{{1}, {2}, {3}, {4}, {5}} {
			payload := make([]byte, 8)
			for i := range payload {
				payload[i] = base[i%len(base)] ^ byte(seed) ^ byte(tr.n) ^ byte(c<<4)
			}
			p.blocks = append(p.blocks, blockPlan{
				track: tr.n, relTicks: int16(c & 0x7), payload: payload,
			})
		}
		// The 4th and 5th blocks describe the same instant as the video block so
		// cross-track deviation stays under 1 ms.
		for i := range p.blocks {
			p.blocks[i].relTicks = 0
		}
		out = append(out, p)
	}
	return out
}

func encodeCluster(plan clusterPlan) []byte {
	parts := [][]byte{uintEl(0xE7, plan.timecode)}
	for _, b := range plan.blocks {
		parts = append(parts, simpleBlock(b.track, b.relTicks, b.payload))
	}
	return el(0x1F43B675, parts...)
}

func clusterPayloadLen(plan clusterPlan) int {
	n := len(uintEl(0xE7, plan.timecode))
	for _, b := range plan.blocks {
		n += len(simpleBlock(b.track, b.relTicks, b.payload))
	}
	return n
}

// buildValid assembles one complete file and records structural offsets. The
// layout is deterministic for a given seed.
func buildValid(seed int64, clusters []clusterPlan, trackList []trackSpec, opts buildOpts) (data []byte, offsets map[string]int64) {
	offsets = map[string]int64{}
	header := el(0x1A45DFA3,
		uintEl(0x4286, 1),
		uintEl(0x42F7, 1),
		uintEl(0x42F2, 4),
		uintEl(0x42F3, 8),
		strEl(0x4282, "matroska"),
		uintEl(0x4287, 4),
		uintEl(0x4285, 2),
	)
	offsets["ebml_header"] = 0

	var trackParts [][]byte
	for _, t := range trackList {
		trackParts = append(trackParts, t.encode())
	}
	tracks := el(0x1654AE6B, trackParts...)

	info := el(0x1549A966,
		uintEl(0x2AD7B1, 1_000_000),
		float64El(0x4489, 2000.0),
		strEl(0x4D80, "assetcheck-fixture"),
		strEl(0x5741, "assetcheck-fixture"),
	)
	offsets["info"] = int64(len(header) + len(putID(0x18538067)) + len(putUnknown()))
	offsets["tracks"] = offsets["info"] + int64(len(info))
	// TrackEntry offsets: Tracks header + every earlier TrackEntry's bytes.
	entryBase := offsets["tracks"] + int64(len(putID(0x1654AE6B))) + int64(len(putSize(len(bytes.Join(trackParts, nil)))))
	running := entryBase
	for i, part := range trackParts {
		offsets[fmt.Sprintf("track%d", i+1)] = running
		running += int64(len(part))
	}
	offset := offsets["tracks"] + int64(len(tracks))

	var clusterBytes [][]byte
	for i, c := range clusters {
		clusterStart := offset
		var body []byte
		if c.unknown {
			body = append(putID(0x1F43B675), putUnknown()...)
			body = append(body, uintEl(0xE7, c.timecode)...)
		} else {
			body = append(putID(0x1F43B675), putSize(clusterPayloadLen(c))...)
			body = append(body, uintEl(0xE7, c.timecode)...)
		}
		offsets[fmt.Sprintf("cluster%d", i)] = clusterStart
		for bi, b := range c.blocks {
			offsets[fmt.Sprintf("cluster%d_block%d", i, bi)] = clusterStart + int64(len(body))
			body = append(body, simpleBlock(b.track, b.relTicks, b.payload)...)
		}
		offset += int64(len(body))
		clusterBytes = append(clusterBytes, body)
	}
	cues := el(0x1C53BB6B,
		el(0xBB,
			uintEl(0xB3, clusters[0].timecode),
			el(0xB7, uintEl(0xF7, 1), uintEl(0xF1, 0)),
		),
	)
	offsets["cues"] = offset

	segBody := bytes.Join(append([][]byte{info, tracks}, append(clusterBytes, cues)...), nil)
	segment := append(putID(0x18538067), putUnknown()...)
	segment = append(segment, segBody...)
	data = append(header, segment...)
	offsets["file_size"] = int64(len(data))
	return data, offsets
}

type buildOpts struct {
	footer bool // append a second EBML header to exercise top-level close
}

// Generate writes every fixture into dir and returns the expectations. The same
// seed always produces byte-identical files.
func Generate(dir string, seed int64) (*Result, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	res := &Result{Dir: dir, Offsets: map[string]int64{}}
	trackList := defaultTracks()
	clusters := defaultClusters(seed)

	valid, offsets := buildValid(seed, clusters, trackList, buildOpts{footer: true})
	if err := write(dir, ValidMKV, valid); err != nil {
		return nil, err
	}
	for k, v := range offsets {
		res.Offsets[k] = v
	}

	// Unknown-length parent: the first cluster keeps an unknown size and must
	// close at the Cues sibling.
	unknownClusters := defaultClusters(seed)
	unknownClusters[0].unknown = true
	unknownData, unknownOffsets := buildValid(seed, unknownClusters, trackList, buildOpts{})
	if err := write(dir, UnknownSizeMKV, unknownData); err != nil {
		return nil, err
	}
	res.Offsets["unknown_cluster"] = unknownOffsets["cluster0"]

	// Truncated element: cut one byte into the Cues element's payload so its
	// declared size cannot be satisfied. The reader reports the Cues ID offset.
	trunc := append([]byte(nil), valid...)
	cutAt := int(offsets["cues"]) + 5
	trunc = trunc[:cutAt]
	if err := write(dir, TruncatedMKV, trunc); err != nil {
		return nil, err
	}
	res.Expectations = append(res.Expectations, Expectation{
		File: TruncatedMKV, Category: "truncated_element", ElementID: 0x1C53BB6B,
		Offset: offsets["cues"], Stage: "parse",
		Note: "file ends inside the Cues element that begins at this offset",
	})

	// Illegal length: a five-byte element ID cannot be a valid Matroska ID.
	illegal := append([]byte(nil), valid[:offsets["tracks"]]...)
	illegal = append(illegal, 0x08, 0x00, 0x00, 0x00, 0x01, 0x81, 0x00)
	illegal = append(illegal, valid[offsets["tracks"]:]...)
	illegalOffset := offsets["tracks"]
	if err := write(dir, IllegalLenMKV, illegal); err != nil {
		return nil, err
	}
	res.Expectations = append(res.Expectations, Expectation{
		File: IllegalLenMKV, Category: "illegal_length", ElementID: 0,
		Offset: illegalOffset, Stage: "parse",
		Note: "five-byte element ID inserted where the Tracks element began",
	})

	// Track metadata conflict: a second video track whose colour metadata
	// disagrees with track 1. The manifest points both tracks at one CICP triple,
	// so the conflict is detectable without external context and is reported at
	// track 1's TrackEntry offset (the earliest locatable one).
	conflictTracks := defaultTracks()
	conflictTracks = append(conflictTracks, trackSpec{
		number: 6, typ: 1, codec: "V_MPEG4/ISO/AVC", lang: "eng", name: "alt",
		video: &videoSpec{w: 1920, h: 1080, colour: &colourSpec{
			present: true, matrix: 1, transfer: 1, primaries: 1, rangeVal: 1,
			bits: 8, chromaH: 1, chromaV: 1}},
	})
	conflictData, conflictOffsets := buildValid(seed, clusters, conflictTracks, buildOpts{})
	if err := write(dir, TrackConflictMKV, conflictData); err != nil {
		return nil, err
	}
	res.Expectations = append(res.Expectations, Expectation{
		File: TrackConflictMKV, Category: "track_metadata_conflict", ElementID: 0xAE,
		Offset: conflictOffsets["track1"], Stage: "validate",
		Note: "track 6 declares CICP 1/1/1 while track 1 declares 9/16/9",
	})

	// Manifest variant that maps the conflicting track so verify can reproduce
	// the conflict: the shipped v1 manifest names only track 1, while this file
	// also carries track 6.
	res.Offsets["conflict_track6"] = conflictOffsets["track6"]

	// Fragment order: track 2's first block sits 1 ms after the cluster
	// reference (inside tolerance) and a second block repeats the track one tick
	// earlier, so that track's time runs backwards without leaving the timeline.
	orderClusters := defaultClusters(seed)
	c0 := orderClusters[0]
	c0.blocks[1].relTicks = 1
	back := c0.blocks[1]
	back.relTicks = 0
	c0.blocks = append(c0.blocks, back)
	orderClusters[0] = c0
	orderData, orderOffsets := buildValid(seed, orderClusters, trackList, buildOpts{})
	if err := write(dir, FragOrderMKV, orderData); err != nil {
		return nil, err
	}
	res.Expectations = append(res.Expectations, Expectation{
		File: FragOrderMKV, Category: "order_violation", ElementID: 0xA3,
		Offset: orderOffsets["cluster0_block5"], Stage: "validate",
		Note: "track 2's second block in cluster 0 repeats one tick earlier than the first",
	})

	mv1 := manifestV1JSON(seed)
	mv2 := manifestV2JSON(seed)
	if err := write(dir, ManifestV1, []byte(mv1)); err != nil {
		return nil, err
	}
	if err := write(dir, ManifestV2, []byte(mv2)); err != nil {
		return nil, err
	}
	rtp := rtpDump(seed)
	if err := write(dir, RTPDump, []byte(rtp)); err != nil {
		return nil, err
	}
	sidecar, err := buildMP4Sidecar()
	if err != nil {
		return nil, err
	}
	if err := write(dir, MP4Sidecar, sidecar); err != nil {
		return nil, err
	}

	sort.Slice(res.Expectations, func(i, j int) bool {
		return res.Expectations[i].File < res.Expectations[j].File
	})
	if err := writeExpectations(dir, res); err != nil {
		return nil, err
	}
	return res, nil
}

// ExpectationsFile is written next to the fixtures so a later verify run can
// compare its report against the generator's preset offsets byte for byte.
const ExpectationsFile = "expectations.json"

func writeExpectations(dir string, res *Result) error {
	data, err := json.MarshalIndent(res.Expectations, "", "  ")
	if err != nil {
		return err
	}
	return write(dir, ExpectationsFile, append(data, '\n'))
}

// LoadExpectations reads the preset failures written beside the fixtures.
func LoadExpectations(dir string) ([]Expectation, error) {
	data, err := os.ReadFile(filepath.Join(dir, ExpectationsFile))
	if err != nil {
		return nil, err
	}
	var out []Expectation
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func write(dir, name string, data []byte) error {
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

// findTrackEntryOffset locates the TrackEntry whose TrackNumber matches. The
// generator knows the layout, so it can scan for the TrackNumber payload rather
// than trusting a parser.
// buildMP4Sidecar assembles a minimal MP4 index sidecar with go-mp4's
// marshaller: ftyp, mvhd, and one audio track whose mdhd timescale and stts
// table define the sample timeline. Element and box headers are written by the
// library, so the sidecar is a real MP4 rather than a private format.
func buildMP4Sidecar() ([]byte, error) {
	payload := func(src mp4.IImmutableBox) ([]byte, error) {
		ws := &writerseeker.WriterSeeker{}
		if _, err := mp4.Marshal(ws, src, mp4.Context{}); err != nil {
			return nil, err
		}
		// BytesReader is a one-shot reader over the written payload; its length
		// already equals the marshalled size for these small boxes.
		br := ws.BytesReader()
		buf := make([]byte, br.Len())
		if br.Len() == 0 {
			return buf, nil
		}
		if _, err := br.Read(buf); err != nil {
			return nil, err
		}
		return buf, nil
	}
	wrap := func(typ string, parts ...[]byte) []byte {
		n := 8
		for _, p := range parts {
			n += len(p)
		}
		out := make([]byte, 8, n)
		binary.BigEndian.PutUint32(out[0:4], uint32(n))
		copy(out[4:8], typ)
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	marshal := func(src mp4.IImmutableBox) ([]byte, error) { return payload(src) }

	mvhd, err := marshal(&mp4.Mvhd{Timescale: 1000, DurationV0: 2000,
		Rate: 0x10000, Volume: 0x100, NextTrackID: 2})
	if err != nil {
		return nil, err
	}
	hdlr, err := marshal(&mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler"})
	if err != nil {
		return nil, err
	}
	mdhd, err := marshal(&mp4.Mdhd{Timescale: 48000, DurationV0: 96000,
		Language: [3]byte{'j', 'p', 'n'}})
	if err != nil {
		return nil, err
	}
	// EntryCount must equal len(Entries); the entry covers four samples.
	stts, err := marshal(&mp4.Stts{EntryCount: 1,
		Entries: []mp4.SttsEntry{{SampleCount: 4, SampleDelta: 24000}}})
	if err != nil {
		return nil, err
	}
	tkhd, err := marshal(&mp4.Tkhd{TrackID: 2, DurationV0: 2000, Volume: 0x100})
	if err != nil {
		return nil, err
	}
	ftyp := wrap("ftyp", []byte("isom\x00\x00\x02\x00isomiso2"))
	stbl := wrap("stbl", wrap("stts", stts))
	mdia := wrap("mdia", wrap("hdlr", hdlr), wrap("mdhd", mdhd), wrap("minf", stbl))
	trak := wrap("trak", wrap("tkhd", tkhd), mdia)
	moov := wrap("moov", wrap("mvhd", mvhd), trak)
	return append(ftyp, moov...), nil
}

// HeaderForTest returns a standalone EBML header with the given DocType, used by
// adapter tests that need a well-formed but unsupported container.
func HeaderForTest(docType string) []byte {
	return el(0x1A45DFA3,
		uintEl(0x4286, 1),
		uintEl(0x42F7, 1),
		strEl(0x4282, docType),
		uintEl(0x4287, 4),
		uintEl(0x4285, 2),
	)
}

func manifestV1JSON(seed int64) string {
	return fmt.Sprintf(`{
  "version": 1,
  "asset_id": "game-ep01-seed%d",
  "title": "Episode 1",
  "languages": ["jpn", "eng"],
  "max_channels": 8,
  "require_colour": true,
  "sequence_base": 0,
  "audio": {
    "2": {"rate_hz": {"min": 48000, "max": 48000}, "layout": "stereo"},
    "3": {"rate_hz": {"min": 48000, "max": 48000}, "layout": "5.1"}
  },
  "video": {
    "1": {"colour": {"matrix": 9, "transfer": 16, "primaries": 9, "range": 1}}
  },
  "tracks": [
    {"number": 1, "type": "video", "codec_id": "V_MPEG4/ISO/AVC", "language": "jpn", "name": "main"},
    {"number": 2, "type": "audio", "codec_id": "A_OPUS", "language": "jpn", "name": "dialogue"},
    {"number": 3, "type": "audio", "codec_id": "A_OPUS", "language": "eng", "name": "dialogue"},
    {"number": 4, "type": "subtitle", "codec_id": "S_TEXT/UTF8", "language": "eng", "name": "dialogue"},
    {"number": 5, "type": "subtitle", "codec_id": "S_TEXT/ASS", "language": "jpn", "name": "signs"}
  ]
}
`, seed)
}

func manifestV2JSON(seed int64) string {
	return fmt.Sprintf(`{
  "version": 2,
  "asset_id": "game-ep01-seed%d",
  "title": "Episode 1",
  "languages": ["jpn", "eng"],
  "max_channels": 8,
  "require_colour": true,
  "sequence_base": 0,
  "audio": {
    "2": {"sample_rate_hz": {"min_hz": 48000, "max_hz": 48000}, "channel_layout": "stereo"},
    "3": {"sample_rate_hz": {"min_hz": 48000, "max_hz": 48000}, "channel_layout": "5.1"}
  },
  "video": {
    "1": {"colour": {"matrix_coefficients": 9, "transfer_characteristics": 16, "primaries": 9, "range": 1}}
  },
  "tracks": [
    {"number": 1, "type": "video", "codec_id": "V_MPEG4/ISO/AVC", "language": "jpn", "name": "main"},
    {"number": 2, "type": "audio", "codec_id": "A_OPUS", "language": "jpn", "name": "dialogue"},
    {"number": 3, "type": "audio", "codec_id": "A_OPUS", "language": "eng", "name": "dialogue"},
    {"number": 4, "type": "subtitle", "codec_id": "S_TEXT/UTF8", "language": "eng", "name": "dialogue"},
    {"number": 5, "type": "subtitle", "codec_id": "S_TEXT/ASS", "language": "jpn", "name": "signs"}
  ]
}
`, seed)
}

// rtpDump writes one line per fragment for tracks 2 and 3, matching the cluster
// times so the RTP start aligns with the media fragments.
func rtpDump(seed int64) string {
	var b bytes.Buffer
	for c := 0; c < 2; c++ {
		// Cluster c starts at c seconds; at 48 kHz that is c*48000 ticks.
		ts := uint32(c) * 48000
		for _, tr := range []struct {
			ssrc uint32
			tag  byte
		}{{0x2222, 2}, {0x3333, 3}} {
			for i := 0; i < 2; i++ {
				seq := uint32(c*2 + i)
				fmt.Fprintf(&b, "%d %d %d 48000\n", tr.ssrc, seq, ts+uint32(i)*960)
			}
		}
	}
	return b.String()
}

func mathFloat64bits(f float64) uint64 { return math.Float64bits(f) }
