// Package fixture generates the DASH, HLS, corrupt-segment and
// checksum-mismatch inputs used by the demo and by the subsystem tests. No
// external media files are referenced: every byte is produced here.
package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediabatch/internal/clock"
)

// Layout is the generated input tree description returned to the CLI.
type Layout struct {
	Root     string
	InputDir string
	DASHDir  string
	HLSDir   string
	InitPath string
	Segments int
	Reps     []string
	Manifest []string
	// Expect maps a relative segment path to its integrity record.
	Expect map[string]Expectation
}

// Expectation records the generated digest and size of a segment file.
type Expectation struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Options controls generation.
type Options struct {
	// Chunk is the batch chunk size used in summaries only.
	Chunk int
	// SegmentCount is the number of segments per representation.
	SegmentCount int
}

// segmentPlan is one generated media segment.
type segmentPlan struct {
	Rep     string
	Index   int
	Start   clock.Time
	End     clock.Time
	Payload []byte
	Samples int
}

// Generate writes the input tree and the integrity manifest under workDir/input.
// Six anomalies are planted, each at a known location:
//   - a truncated DASH fragment (high and low, index 3)
//   - a hash mismatch on the low representation (index 1)
//   - an HLS EXTINF with a missing duration (index 5)
//   - a duplicate HLS segment reference (index 7)
//   - an unknown HLS tag
//   - a non-keyframe first sample right after the HLS discontinuity
func Generate(workDir string, opts Options) (*Layout, error) {
	if opts.SegmentCount <= 0 {
		opts.SegmentCount = 12
	}
	inputDir := filepath.Join(workDir, "input")
	dashDir := filepath.Join(inputDir, "dash")
	hlsDir := filepath.Join(inputDir, "hls")
	for _, d := range []string{dashDir, filepath.Join(dashDir, "v720"), filepath.Join(dashDir, "v360"), hlsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	layout := &Layout{
		Root: workDir, InputDir: inputDir, DASHDir: dashDir, HLSDir: hlsDir,
		Expect: map[string]Expectation{},
		Reps:   []string{"v720", "v360"},
	}

	const timescale uint32 = 1000
	const segDur = 4 * 1000 // 4 s in ticks
	initData, err := BuildInit(timescale, 1, "iso6")
	if err != nil {
		return nil, fmt.Errorf("init: %w", err)
	}
	initPath := filepath.Join(dashDir, "init.mp4")
	if err := writeFile(initPath, initData); err != nil {
		return nil, err
	}
	layout.InitPath = initPath

	plans := make([]segmentPlan, 0, opts.SegmentCount*2)
	for repIdx, rep := range layout.Reps {
		for i := 0; i < opts.SegmentCount; i++ {
			start := clock.MustNew(int64(i*4), 1)
			end := clock.MustNew(int64((i+1)*4), 1)
			samples := 4
			if repIdx == 1 && i != 0 {
				samples = 2
			}
			payload := deterministicPayload(fmt.Sprintf("%s-%d", rep, i), 96+repIdx*32+4*i)
			plans = append(plans, segmentPlan{Rep: rep, Index: i, Start: start, End: end, Payload: payload, Samples: samples})
		}
	}

	// Shared content between representations for exact-time dedup: v720 and
	// v360 index 0 are byte-identical at the same time range.
	shared := deterministicPayload("shared-0", 64)
	for i := range plans {
		if plans[i].Index == 0 {
			plans[i].Payload = append([]byte(nil), shared...)
		}
	}

	for _, p := range plans {
		spec := fragmentSpec{
			Sequence:   uint32(p.Index + 1),
			TrackID:    1,
			BaseDecode: uint64(p.Index) * segDur,
			Timescale:  timescale,
			Payload:    p.Payload,
			NonSync:    false,
		}
		for s := 0; s < p.Samples; s++ {
			spec.Samples = append(spec.Samples, sampleSpec{
				Duration: segDur / uint32(p.Samples),
				Size:     uint32(len(p.Payload)) / uint32(p.Samples),
				Sync:     s == 0,
			})
		}
		data, err := BuildFragment(spec)
		if err != nil {
			return nil, fmt.Errorf("fragment %s/%d: %w", p.Rep, p.Index, err)
		}
		if p.Rep == "v360" && p.Index == 3 {
			// Corpus damage: cut the fragment before the end of mdat.
			data = data[:len(data)-17]
		}
		rel := filepath.ToSlash(filepath.Join("dash", p.Rep, fmt.Sprintf("seg-%d.m4s", p.Index+1)))
		if err := writeFile(filepath.Join(inputDir, filepath.FromSlash(rel)), data); err != nil {
			return nil, err
		}
		layout.Expect[rel] = Expectation{SHA256: hashOf(data), Size: int64(len(data))}
		layout.Segments++
	}
	// Corrupt the recorded digest of one intact segment so the scanner reports
	// a content mismatch rather than a size mismatch.
	if e, ok := layout.Expect["dash/v720/seg-2.m4s"]; ok {
		e.SHA256 = strings.Repeat("0", 64)
		layout.Expect["dash/v720/seg-2.m4s"] = e
	}

	mpd := dashManifest(opts.SegmentCount)
	mpdPath := filepath.Join(dashDir, "manifest.mpd")
	if err := writeFile(mpdPath, []byte(mpd)); err != nil {
		return nil, err
	}
	layout.Manifest = append(layout.Manifest, mustRel(inputDir, mpdPath))

	hlsInit, err := BuildInit(1000, 2, "iso6")
	if err != nil {
		return nil, fmt.Errorf("hls init: %w", err)
	}
	if err := writeFile(filepath.Join(hlsDir, "init.mp4"), hlsInit); err != nil {
		return nil, err
	}

	hls := hlsPlaylist(opts.SegmentCount)
	hlsPath := filepath.Join(hlsDir, "prog.m3u8")
	if err := writeFile(hlsPath, []byte(hls)); err != nil {
		return nil, err
	}
	layout.Manifest = append(layout.Manifest, mustRel(inputDir, hlsPath))

	// HLS media segments are real MP4 fragments so the scanner can validate
	// them. Segment 5 (index 4) follows the discontinuity with a non-keyframe
	// first sample, which the scanner reports as a boundary anomaly.
	for i := 0; i < opts.SegmentCount; i++ {
		const hlsTimescale uint32 = 1000
		payload := deterministicPayload(fmt.Sprintf("hls-%d", i), 64)
		spec := fragmentSpec{
			Sequence:   uint32(i + 1),
			TrackID:    1,
			BaseDecode: uint64(i) * segDur,
			Timescale:  hlsTimescale,
			Payload:    payload,
			NonSync:    i == 4,
		}
		for s := 0; s < 4; s++ {
			spec.Samples = append(spec.Samples, sampleSpec{
				Duration: segDur / 4,
				Size:     uint32(len(payload) / 4),
			})
		}
		data, err := BuildFragment(spec)
		if err != nil {
			return nil, fmt.Errorf("hls fragment %d: %w", i, err)
		}
		rel := filepath.ToSlash(filepath.Join("hls", fmt.Sprintf("seg-%d.m4s", i+1)))
		if err := writeFile(filepath.Join(inputDir, filepath.FromSlash(rel)), data); err != nil {
			return nil, err
		}
		layout.Expect[rel] = Expectation{SHA256: hashOf(data), Size: int64(len(data))}
		layout.Segments++
	}

	// Persist the expectations so resume/verify runs see the same records.
	if err := writeExpectations(filepath.Join(inputDir, "expectations.json"), layout.Expect); err != nil {
		return nil, err
	}
	return layout, nil
}

func dashManifest(n int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT`)
	fmt.Fprintf(&b, "%d", n*4)
	b.WriteString(`S" profiles="urn:mpeg:dash:profile:isoff-live:2011">
  <Period id="p0" start="PT0S">
    <AdaptationSet contentType="video" mimeType="video/mp4">
      <Representation id="v720" bandwidth="2000000" width="1280" height="720">
        <SegmentTemplate timescale="1000" initialization="init.mp4" media="v720/seg-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
`)
	for i := 0; i < n; i++ {
		if i == 0 {
			fmt.Fprintf(&b, "            <S t=\"0\" d=\"4000\" r=\"0\"/>\n")
		} else {
			fmt.Fprintf(&b, "            <S d=\"4000\" r=\"0\"/>\n")
		}
	}
	b.WriteString(`          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
      <Representation id="v360" bandwidth="700000" width="640" height="360">
        <SegmentTemplate timescale="1000" initialization="init.mp4" media="v360/seg-$Number$.m4s" startNumber="1">
          <SegmentTimeline>
`)
	for i := 0; i < n; i++ {
		if i == 0 {
			fmt.Fprintf(&b, "            <S t=\"0\" d=\"4000\" r=\"0\"/>\n")
		} else {
			fmt.Fprintf(&b, "            <S d=\"4000\" r=\"0\"/>\n")
		}
	}
	b.WriteString(`          </SegmentTimeline>
        </SegmentTemplate>
      </Representation>
    </AdaptationSet>
  </Period>
</MPD>
`)
	return b.String()
}

func hlsPlaylist(n int) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n")
	for i := 0; i < n; i++ {
		switch {
		case i == n-1:
			// Planted anomaly: the final EXTINF carries no duration. No timing
			// is invented for it, so the URI is reported and not published.
			b.WriteString("#EXTINF:,\n")
		case i == 4:
			// The discontinuity precedes segment 5, where the first sample is
			// deliberately not a keyframe.
			b.WriteString("#EXT-X-DISCONTINUITY\n#EXTINF:4.000,\n")
		default:
			b.WriteString("#EXTINF:4.000,\n")
		}
		fmt.Fprintf(&b, "seg-%d.m4s\n", i+1)
		if i == 0 {
			// Planted anomaly: a second reference to segment 1. It is isolated
			// and consumes no time, so later segments keep their positions.
			b.WriteString("#EXTINF:4.000,\nseg-1.m4s\n")
		}
	}
	// Planted anomaly: an unknown tag between segments.
	b.WriteString("#EXT-X-CUSTOM-NOTE:generated\n")
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

func writeExpectations(path string, m map[string]Expectation) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// LoadExpectations reads the integrity records written by Generate.
func LoadExpectations(path string) (map[string]Expectation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]Expectation
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// DetectLayout rebuilds the layout description for an already-generated input
// tree, so a repeated demo run does not regenerate fixtures.
func DetectLayout(inputDir string) (*Layout, error) {
	l := &Layout{
		Root: filepath.Dir(inputDir), InputDir: inputDir,
		DASHDir:  filepath.Join(inputDir, "dash"),
		HLSDir:   filepath.Join(inputDir, "hls"),
		InitPath: filepath.Join(inputDir, "dash", "init.mp4"),
		Reps:     []string{"v720", "v360"},
		Expect:   map[string]Expectation{},
	}
	for _, rel := range []string{"dash/manifest.mpd", "hls/prog.m3u8"} {
		p := filepath.Join(inputDir, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err != nil {
			return nil, err
		}
		l.Manifest = append(l.Manifest, rel)
	}
	exp, err := LoadExpectations(filepath.Join(inputDir, "expectations.json"))
	if err != nil {
		return nil, err
	}
	l.Expect = exp
	// Count media segments only; init.mp4 and the manifests are not segments.
	for rel := range exp {
		if strings.HasSuffix(rel, ".m4s") {
			l.Segments++
		}
	}
	return l, nil
}
