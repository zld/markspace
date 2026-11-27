package mediacore

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/pion/rtp"
)

// Scenario is the fully generated, in-memory demo input set. Everything lives
// in maps keyed by stable strings, so the kernel never touches the filesystem
// or network during a run.
type Scenario struct {
	Seed      int64
	Manifests map[string]string // manifest name -> manifest text
	Files     map[string][]byte // segment uri -> fMP4 bytes
	RTP       map[string][]byte // rtp capture uri -> length-prefixed capture
	Missing   map[string]bool   // uris that must fail to load
	Order     []string          // stable asset order for reporting
	Flags     map[string]map[string]string
}

// FixtureBuilder accumulates generated artifacts with a seeded RNG so two runs
// with the same seed produce byte-identical inputs.
type FixtureBuilder struct {
	rng     *rand.Rand
	seed    int64
	sc      *Scenario
	counter int
}

// NewFixtureBuilder starts generation for the given seed.
func NewFixtureBuilder(seed int64) *FixtureBuilder {
	return &FixtureBuilder{rng: rand.New(rand.NewSource(seed)), seed: seed,
		sc: &Scenario{Seed: seed, Manifests: map[string]string{}, Files: map[string][]byte{},
			RTP: map[string][]byte{}, Missing: map[string]bool{}, Flags: map[string]map[string]string{}}}
}

func (b *FixtureBuilder) payload(tag string, n int) []byte {
	b.counter++
	out := make([]byte, n)
	base := []byte(fmt.Sprintf("seed%d-%s-%d|", b.seed, tag, b.counter))
	for i := range out {
		out[i] = base[i%len(base)] ^ byte(b.rng.Intn(256))
	}
	return out
}

func (b *FixtureBuilder) frag(uri string, track uint32, ts uint32, base uint64,
	deltas []uint32, color *ColorInfo, payload []byte) {
	raw, err := BuildFragment(FragmentSpec{TrackID: track, Timescale: ts, BaseDecode: base,
		SampleDeltas: deltas, Payload: payload, Color: color})
	if err != nil {
		panic(err) // generator bug, not a runtime input failure
	}
	b.sc.Files[uri] = raw
}

func (b *FixtureBuilder) annotate(asset, k, v string) {
	if b.sc.Flags[asset] == nil {
		b.sc.Flags[asset] = map[string]string{}
	}
	b.sc.Flags[asset][k] = v
}

// BuildScenario generates every required fixture class.
func BuildScenario(seed int64) *Scenario {
	b := NewFixtureBuilder(seed)
	b.buildComplete() // complete, two versions, identical media, differing color
	b.buildMissing()  // one missing segment
	b.buildConflict() // same slot, genuinely different payloads
	b.buildRate()     // sample-rate conflict, no explicit target
	b.buildIllegal()  // overflowing/illegal duration, rejected at parse
	b.buildCycle()    // dependency cycle, rejected at graph
	b.buildRTPGap()   // RTP sequence gap -> anomaly + rollback
	sort.Strings(b.sc.Order)
	return b.sc
}

// buildComplete: one track, two segment slots, two versions covering both.
// Media payloads are identical so the merge is idempotent; colr boxes differ so
// color arbitration by explicit priority is exercised. Both slots commit.
func (b *FixtureBuilder) buildComplete() {
	const asset = "asset-complete"
	b.sc.Order = append(b.sc.Order, asset)
	ts := uint32(90000)
	media := [2][]byte{b.payload("complete", 64), b.payload("complete", 64)}
	colrV1 := &ColorInfo{Present: true, Primaries: 1, Transfer: 1, Matrix: 1}
	colrV2 := &ColorInfo{Present: true, Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true}
	for i, p := range media {
		base := uint64(i) * 90000
		b.frag(fmt.Sprintf("complete/v1/seg%d.m4s", i), 1, ts, base, []uint32{90000}, colrV1, p)
		b.frag(fmt.Sprintf("complete/v2/seg%d.m4s", i), 1, ts, base, []uint32{90000}, colrV2, p)
	}
	segs := []hlsSeg{{Seq: 0, URI: "PLACEHOLDER0", Dur: 1.0}, {Seq: 1, URI: "PLACEHOLDER1", Dur: 1.0}}
	v1 := withURIs(segs, []string{"complete/v1/seg0.m4s", "complete/v1/seg1.m4s"})
	v2 := withURIs(segs, []string{"complete/v2/seg0.m4s", "complete/v2/seg1.m4s"})
	b.sc.Manifests[asset+"-v1"] = hlsManifest(asset, "v1", 1, "video", ts, v1, hlsOpt{colorPrio: 3})
	b.sc.Manifests[asset+"-v2"] = hlsManifest(asset, "v2", 1, "video", ts, v2, hlsOpt{colorPrio: 5})
	b.annotate(asset, "class", "complete")
}

func withURIs(segs []hlsSeg, uris []string) []hlsSeg {
	out := make([]hlsSeg, len(segs))
	copy(out, segs)
	for i := range out {
		out[i].URI = uris[i]
	}
	return out
}

// buildMissing: a manifest references one segment whose file is absent.
func (b *FixtureBuilder) buildMissing() {
	const asset = "asset-missing"
	b.sc.Order = append(b.sc.Order, asset)
	ts := uint32(48000)
	b.sc.Missing["missing/seg0.m4s"] = true
	b.sc.Manifests[asset] = hlsManifest(asset, "v1", 2, "video", ts,
		[]hlsSeg{{Seq: 0, URI: "missing/seg0.m4s", Dur: 0.02}}, hlsOpt{})
	b.annotate(asset, "class", "missing")
}

// buildConflict: same slot in both versions, different media payloads.
func (b *FixtureBuilder) buildConflict() {
	const asset = "asset-conflict"
	b.sc.Order = append(b.sc.Order, asset)
	ts := uint32(90000)
	b.frag("conflict/v1/seg0.m4s", 3, ts, 0, []uint32{45000}, nil, b.payload("cf-a", 48))
	b.frag("conflict/v2/seg0.m4s", 3, ts, 0, []uint32{45000}, nil, b.payload("cf-B-different", 48))
	b.sc.Manifests[asset+"-v1"] = hlsManifest(asset, "v1", 3, "video", ts,
		[]hlsSeg{{Seq: 0, URI: "conflict/v1/seg0.m4s", Dur: 0.5}}, hlsOpt{})
	b.sc.Manifests[asset+"-v2"] = hlsManifest(asset, "v2", 3, "video", ts,
		[]hlsSeg{{Seq: 0, URI: "conflict/v2/seg0.m4s", Dur: 0.5}}, hlsOpt{})
	b.annotate(asset, "class", "conflict")
}

// buildRate: two audio versions with different sample rates and no target.
func (b *FixtureBuilder) buildRate() {
	const asset = "asset-rate"
	b.sc.Order = append(b.sc.Order, asset)
	b.frag("rate/v1/seg0.m4s", 4, 48000, 0, []uint32{1024}, nil, b.payload("rate-a", 24))
	b.frag("rate/v2/seg0.m4s", 4, 44100, 0, []uint32{1024}, nil, b.payload("rate-b", 24))
	b.sc.Manifests[asset+"-v1"] = hlsManifest(asset, "v1", 4, "audio", 48000,
		[]hlsSeg{{Seq: 0, URI: "rate/v1/seg0.m4s", Dur: 0.0213}},
		hlsOpt{rate: 48000, layout: "stereo"})
	b.sc.Manifests[asset+"-v2"] = hlsManifest(asset, "v2", 4, "audio", 44100,
		[]hlsSeg{{Seq: 0, URI: "rate/v2/seg0.m4s", Dur: 0.0232}},
		hlsOpt{rate: 44100, layout: "stereo"})
	b.annotate(asset, "class", "rate-conflict")
}

// buildIllegal: an EXTINF duration that overflows the nanosecond range must be
// rejected at the playlist stage; nothing from this asset is ever committed.
func (b *FixtureBuilder) buildIllegal() {
	const asset = "asset-illegal"
	b.sc.Order = append(b.sc.Order, asset)
	b.sc.Manifests[asset] = "#EXTM3U\n" +
		"#EXT-X-MEDIACORE-TRACK:ID=5,KIND=video,TIMESCALE=90000,VERSION=v1\n" +
		"#EXTINF:99999999999999999999.000,SEQ=0\nillegal/seg0.m4s\n"
	b.annotate(asset, "class", "illegal-duration")
}

// buildCycle: two tracks depending on each other; both are rejected as a set.
func (b *FixtureBuilder) buildCycle() {
	const asset = "asset-cycle"
	b.sc.Order = append(b.sc.Order, asset)
	ts := uint32(90000)
	b.frag("cycle/seg0.m4s", 6, ts, 0, []uint32{90000}, nil, b.payload("cyc", 32))
	track := func(id, dep uint32) string {
		return fmt.Sprintf("#EXT-X-MEDIACORE-TRACK:ID=%d,KIND=video,TIMESCALE=%d,VERSION=v1,DEPENDS=%d\n", id, ts, dep) +
			"#EXTINF:1.000,SEQ=0\ncycle/seg0.m4s\n"
	}
	b.sc.Manifests[asset] = "#EXTM3U\n" + track(6, 7) + track(7, 6)
	b.annotate(asset, "class", "cycle")
}

// buildRTPGap: one fMP4 segment whose sibling RTP capture contains a sequence
// gap. Anomaly detection aborts the slot and produces a rollback record.
func (b *FixtureBuilder) buildRTPGap() {
	const asset = "asset-rtp"
	b.sc.Order = append(b.sc.Order, asset)
	ts := uint32(90000)
	b.frag("rtp/seg0.m4s", 8, ts, 0, []uint32{90000}, nil, b.payload("rtp-seg", 32))
	var pkts []*rtp.Packet
	ssrc := uint32(777)
	for seq := uint16(20); seq < 24; seq++ {
		pkts = append(pkts, NewRTPPacket(ssrc, seq, uint32(seq)*960, byte(seq)))
	}
	pkts = append(pkts, NewRTPPacket(ssrc, 27, 27*960, 27)) // gap: 24,25,26
	raw, err := BuildRTPCapture(pkts)
	if err != nil {
		panic(err)
	}
	b.sc.RTP["rtp/cap.bin"] = raw
	b.sc.Manifests[asset] = hlsManifest(asset, "v1", 8, "video", ts,
		[]hlsSeg{{Seq: 0, URI: "rtp/seg0.m4s", Dur: 1.0, RTPURI: "rtp/cap.bin"}}, hlsOpt{})
	b.annotate(asset, "class", "rtp-gap")
}

// ---- manifest text helpers ----

type hlsSeg struct {
	Seq    uint32
	URI    string
	Dur    float64
	RTPURI string
}

type hlsOpt struct {
	rate      uint32
	layout    string
	colorPrio uint32
}

func hlsManifest(asset, version string, id uint32, kind string, ts uint32, segs []hlsSeg, opt hlsOpt) string {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString(fmt.Sprintf("#EXT-X-MEDIACORE-ASSET:ID=%s\n", asset))
	sb.WriteString(fmt.Sprintf("#EXT-X-MEDIACORE-TRACK:ID=%d,KIND=%s,TIMESCALE=%d,VERSION=%s",
		id, kind, ts, version))
	if opt.rate != 0 {
		sb.WriteString(fmt.Sprintf(",RATE=%d,LAYOUT=%s", opt.rate, opt.layout))
	}
	if opt.colorPrio != 0 {
		sb.WriteString(fmt.Sprintf(",COLOR-PRIO=%d", opt.colorPrio))
	}
	sb.WriteString("\n")
	for _, s := range segs {
		title := fmt.Sprintf("SEQ=%d", s.Seq)
		if s.RTPURI != "" {
			title += ",KIND=rtp,RTP=" + s.RTPURI
		}
		sb.WriteString(fmt.Sprintf("#EXTINF:%.3f,%s\n%s\n", s.Dur, title, s.URI))
	}
	return sb.String()
}

// MemLoader serves generated bytes from the Scenario. A missing uri returns a
// sourced ErrMissingSegment; present uris are parsed through InspectSegment.
type MemLoader struct {
	sc *Scenario
}

// NewMemLoader wires a scenario into the Loader interface.
func NewMemLoader(sc *Scenario) *MemLoader { return &MemLoader{sc: sc} }

// Load implements Loader.
func (m *MemLoader) Load(asset string, tr *TrackDecl, ref *SegmentRef) ([]byte, *SegmentInfo, error) {
	if m.sc.Missing[ref.URI] {
		return nil, nil, WrapMsg(ErrMissingSegment, asset, tr.ID, StageMapping, ref.ByteOff,
			"segment not found: "+ref.URI)
	}
	if rtpURI := ref.Attrs["RTP"]; rtpURI != "" {
		if raw, ok := m.sc.RTP[rtpURI]; ok {
			return raw, nil, nil
		}
		return nil, nil, WrapMsg(ErrMissingSegment, asset, tr.ID, StageMapping, ref.ByteOff,
			"rtp capture not found: "+rtpURI)
	}
	raw, ok := m.sc.Files[ref.URI]
	if !ok {
		return nil, nil, WrapMsg(ErrMissingSegment, asset, tr.ID, StageMapping, ref.ByteOff,
			"segment not found: "+ref.URI)
	}
	info, err := InspectSegment(asset, tr.ID, tr.Timescale, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	return raw, info, nil
}
