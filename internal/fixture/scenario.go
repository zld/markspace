package fixture

import (
	"fmt"
	"math"
	"strings"
)

// ScenarioConfig selects which structural and timing conditions the
// generator injects. All fields are deterministic.
type ScenarioConfig struct {
	Segments          int
	SamplesPerSegment int
	Timescale         uint32
	VFR               bool
	TimestampWrap     bool
	DriftPPM          float64
	TruncateBoxBytes  int // bytes removed from the target segment's tail
	TruncateTarget    int // segment index to truncate
	MissingSegment    bool
	DuplicateSequence bool
}

// Scenario is one fully generated, self-contained test case.
type Scenario struct {
	Config   ScenarioConfig
	HLS      []byte
	DASH     []byte
	Segments []GeneratedSegment
	// RTPPackets exercise wrap/VFR handling on packet timestamps.
	RTPPackets [][]byte
	RTPRate    uint32
}

// GeneratedSegment is one media chunk plus its manifest identity.
type GeneratedSegment struct {
	Name     string
	MediaSeq uint64
	Data     []byte
	// Present=false models a manifest entry whose bytes never arrived.
	Present bool
	// ManifestOffset is the byte position of the entry in the playlist.
	ManifestOffset int64
}

// DefaultConfig returns a clean 4-segment baseline.
func DefaultConfig() ScenarioConfig {
	return ScenarioConfig{
		Segments: 4, SamplesPerSegment: 4, Timescale: 1000,
		TruncateTarget: 1,
	}
}

// BuildScenario synthesizes manifests and media from the config.
func BuildScenario(cfg ScenarioConfig) (*Scenario, error) {
	if cfg.Segments <= 0 {
		cfg.Segments = 4
	}
	if cfg.Timescale == 0 {
		cfg.Timescale = 1000
	}
	sc := &Scenario{Config: cfg, RTPRate: 90000}

	type built struct {
		seq     uint64
		name    string
		data    []byte
		present bool
	}
	var builtSegs []built
	base := uint64(0)
	if cfg.TimestampWrap {
		// Start close to the 32-bit boundary so later segments cross it.
		base = (1 << 32) - uint64(cfg.Timescale)
	}
	// Drift is accumulated as a float: at 120 ppm a single short segment
	// moves by a fraction of a tick, so rounding per segment would erase
	// the effect entirely. Only the tfdt base value is quantized.
	driftBase := float64(base)
	samplesPerSeg := cfg.SamplesPerSegment
	if samplesPerSeg <= 0 {
		samplesPerSeg = 4
	}
	for i := 0; i < cfg.Segments; i++ {
		samples := make([]Sample, samplesPerSeg)
		segDur := uint64(0)
		for j := range samples {
			dur := uint32(cfg.Timescale / 10) // 100 ms nominal
			if cfg.VFR && j == 2 {
				dur = uint32(float64(dur) * 1.6)
			}
			samples[j] = Sample{
				Duration: dur, Size: uint32(24 + j*5),
				Keyframe: j == 0, Payload: byte(i*11 + j + 1),
			}
			segDur += uint64(dur)
		}
		data := MediaSegment(1, uint32(i), uint64(math.Round(driftBase)), samples)
		name := fmt.Sprintf("seg%d.m4s", i)
		builtSegs = append(builtSegs, built{
			seq: uint64(i), name: name, data: data, present: true,
		})
		// A fast source clock accumulates extra ticks per real segment,
		// so the tfdt base advances by (1+drift) times the true span.
		// The drift estimator must then recover the configured ppm.
		driftBase += float64(segDur) * (1 + cfg.DriftPPM/1e6)
	}

	if cfg.TruncateBoxBytes > 0 && cfg.TruncateTarget < len(builtSegs) {
		t := cfg.TruncateTarget
		cut := len(builtSegs[t].data) - cfg.TruncateBoxBytes
		if cut < 8 {
			cut = 8
		}
		builtSegs[t].data = builtSegs[t].data[:cut]
	}

	// Apply boundary conditions by editing the declared sequence numbers.
	type manifestRow struct {
		seq     uint64
		name    string
		present bool
	}
	rows := make([]manifestRow, 0, len(builtSegs))
	for _, b := range builtSegs {
		rows = append(rows, manifestRow{b.seq, b.name, b.present})
	}
	if cfg.MissingSegment && len(rows) > 2 {
		// Drop the third chunk's bytes while keeping later sequence ids.
		rows[2].present = false
	}
	if cfg.DuplicateSequence && len(rows) > 1 {
		// Re-declare the second entry's sequence id on the third entry.
		rows[2].seq = rows[1].seq
	}

	dataBySeq := map[uint64][]byte{}
	for _, b := range builtSegs {
		dataBySeq[b.seq] = b.data
	}
	for i, r := range rows {
		data := dataBySeq[uint64(i)]
		if cfg.DuplicateSequence && i == 2 {
			data = dataBySeq[1]
		}
		sc.Segments = append(sc.Segments, GeneratedSegment{
			Name: r.name, MediaSeq: r.seq, Data: data,
			Present: r.present, ManifestOffset: int64(i * 48),
		})
	}

	sc.HLS = buildHLS(cfg, sc.Segments)
	sc.DASH = buildDASH(cfg, sc.Segments)
	sc.RTPPackets = buildRTP(cfg)
	return sc, nil
}

func buildHLS(cfg ScenarioConfig, segs []GeneratedSegment) []byte {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	dur := float64(cfg.Timescale/10*4) / float64(cfg.Timescale)
	for _, s := range segs {
		// Explicit sequence tag keeps missing/duplicate conditions
		// detectable from the playlist text alone.
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n#X-MEDIASCAN-SEQ:%d\n%s\n",
			dur, s.MediaSeq, s.Name)
	}
	return []byte(b.String())
}

func buildDASH(cfg ScenarioConfig, segs []GeneratedSegment) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>` + "\n")
	b.WriteString(`<MPD type="static" mediaPresentationDuration="PT4S">` + "\n")
	b.WriteString(`  <Period><AdaptationSet mimeType="video/mp4">` + "\n")
	b.WriteString(`    <Representation id="v0">` + "\n")
	fmt.Fprintf(&b, `      <SegmentTemplate timescale="%d" media="chunk-$Number$.m4s" `+
		"startNumber=\"0\" duration=\"%d\">\n", cfg.Timescale, cfg.Timescale/10*4)
	b.WriteString("        <SegmentTimeline>\n")
	for _, s := range segs {
		fmt.Fprintf(&b, `          <S t="%d" n="%d" d="%d"/>`+"\n",
			s.MediaSeq*uint64(cfg.Timescale/10*4), s.MediaSeq, cfg.Timescale/10*4)
	}
	b.WriteString("        </SegmentTimeline>\n      </SegmentTemplate>\n")
	b.WriteString("    </Representation>\n  </AdaptationSet></Period>\n</MPD>\n")
	return []byte(b.String())
}

func buildRTP(cfg ScenarioConfig) [][]byte {
	deltas := make([]uint32, cfg.Segments)
	start := uint32(0)
	if cfg.TimestampWrap {
		start = uint32(0xFFFFFFFF - 6000)
	}
	for i := range deltas {
		deltas[i] = 3000 // 90 kHz / 30 fps nominal
		if cfg.VFR && i%2 == 1 {
			deltas[i] = 4800
		}
	}
	pkts, err := RTPStream(90000, start, deltas, 2)
	if err != nil {
		return nil
	}
	return pkts
}
