package continuity

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The demo builds two local sources ("main" and "backup") from generated CMAF
// bytes only. No external media is referenced: every fixture is written under
// the run directory and every byte is reproducible.

// DemoSegment is one generated segment on disk.
type DemoSegment struct {
	Source string
	Track  string
	File   string // manifest-relative path
	Init   string // manifest-relative init path
	Start  Ticks  // manifest-declared start (cumulative)
	Dur    Ticks
	Bytes  []byte
}

// DemoFixture is a fully generated two-source demo corpus.
type DemoFixture struct {
	Root        string
	Segments    []DemoSegment
	Inits       map[string][]byte
	HLSFiles    []string
	DASHFiles   []string
	Mappings    []string
	SampleCount int
}

// DemoConfig selects which fixtures to generate.
type DemoConfig struct {
	// Scenario is "parallel" (backup mirrors main) or "switchover" (backup
	// re-delivers stale data and fills a hole after the switch).
	Scenario string
	// TruncateBackup makes one backup fragment a truncated box.
	TruncateBackup bool
}

// Scenario names are stable CLI-facing identifiers.
const (
	ScenarioParallel   = "parallel"
	ScenarioSwitchover = "switchover"
)

// DefaultDemoConfig is the parallel acceptance corpus: 8 video samples per
// source with identical payloads, so the backup is exactly duplicative.
func DefaultDemoConfig() DemoConfig {
	return DemoConfig{Scenario: ScenarioParallel}
}

// SwitchoverDemoConfig is the recovery corpus. Main carries 7 samples with a
// hole at 180000 ticks; backup re-delivers a stale sample at 0 (beyond the
// window) and fills the hole at 180000 (inside the window).
func SwitchoverDemoConfig() DemoConfig {
	return DemoConfig{Scenario: ScenarioSwitchover}
}

// videoFrameTicks is 1/3 s at the 90 kHz base, long enough that a 500 ms late
// window distinguishes inside-window from beyond-window samples.
const videoFrameTicks Ticks = 30000

// BuildDemoPlan returns the deterministic sample plan for a scenario: per
// source, the segments' sample specs plus the media base decode times.
func BuildDemoPlan(cfg DemoConfig) (map[string][][]SampleSpec, map[string][]Ticks) {
	if cfg.Scenario == ScenarioSwitchover {
		mainStarts := []Ticks{0, 30000, 60000, 90000, 120000, 150000, 210000}
		plan := map[string][][]SampleSpec{}
		seeds := []byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27}
		for i, start := range mainStarts {
			plan["main"] = append(plan["main"], []SampleSpec{{
				Data:     pattern(seeds[i], 80+int(start)/1000),
				Duration: uint32(videoFrameTicks),
				Sync:     true,
			}})
		}
		plan["backup"] = [][]SampleSpec{
			{{Data: pattern(0x91, 96), Duration: uint32(videoFrameTicks), Sync: true}},
			{{Data: pattern(0x92, 96), Duration: uint32(videoFrameTicks), Sync: true}},
		}
		return plan, map[string][]Ticks{"main": mainStarts, "backup": {0, 180000}}
	}
	var segs [][]SampleSpec
	var starts []Ticks
	for s := 0; s < 4; s++ {
		start := Ticks(s * 2 * 3000)
		starts = append(starts, start)
		var specs []SampleSpec
		for k := 0; k < 2; k++ {
			idx := s*2 + k
			specs = append(specs, SampleSpec{
				Data:     pattern(byte(0x10+idx), 64+idx),
				Duration: 3000,
				Sync:     idx%2 == 0,
			})
		}
		segs = append(segs, specs)
	}
	plan := map[string][][]SampleSpec{}
	startsBySource := map[string][]Ticks{}
	for _, src := range []string{"main", "backup"} {
		plan[src] = make([][]SampleSpec, len(segs))
		for i := range segs {
			plan[src][i] = append([]SampleSpec(nil), segs[i]...)
		}
		startsBySource[src] = append([]Ticks(nil), starts...)
	}
	return plan, startsBySource
}

// GenerateDemo writes the demo corpus under root and returns its description.
func GenerateDemo(root string, cfg DemoConfig) (*DemoFixture, error) {
	if cfg.Scenario == "" {
		cfg.Scenario = ScenarioParallel
	}
	if cfg.Scenario != ScenarioParallel && cfg.Scenario != ScenarioSwitchover {
		return nil, Usagef("unknown demo scenario %q", cfg.Scenario)
	}
	fx := &DemoFixture{Root: root, Inits: map[string][]byte{}}
	videoTrack := TrackSpec{ID: 1, Kind: MediaVideo, Timescale: 90000, Handler: "vide", SampleEntry: "avc1", CodecConfig: avcCConfig(), Width: 1280, Height: 720}
	for _, src := range []string{"main", "backup"} {
		initPath := src + "/init.mp4"
		data := BuildInit(InitSpec{Tracks: []TrackSpec{videoTrack}})
		fx.Inits[initPath] = data
		if err := writeFile(root, initPath, data); err != nil {
			return nil, err
		}
	}
	plan, startsBySource := BuildDemoPlan(cfg)
	for _, src := range []string{"main", "backup"} {
		segs := plan[src]
		starts := startsBySource[src]
		var cum Ticks
		for i, specs := range segs {
			var dur Ticks
			for _, s := range specs {
				dur += Ticks(s.Duration)
			}
			frag := FragmentSpec{
				BaseDecodeTime: uint64(starts[i]),
				TrackID:        1,
				Timescale:      90000,
				Samples:        specs,
			}
			path := fmt.Sprintf("%s/video/seg-%05d.m4s", src, i)
			data := BuildFragment(frag)
			if src == "backup" && cfg.TruncateBackup && i == 1 {
				frag.TruncateBytes = 16
				data = BuildFragment(frag)
			}
			if err := writeFile(root, path, data); err != nil {
				return nil, err
			}
			fx.Segments = append(fx.Segments, DemoSegment{
				Source: src, Track: "video", File: path, Init: src + "/init.mp4",
				Start: cum, Dur: dur, Bytes: data,
			})
			fx.SampleCount += len(specs)
			cum += dur
		}
	}
	sort.SliceStable(fx.Segments, func(i, j int) bool {
		if fx.Segments[i].Source != fx.Segments[j].Source {
			return fx.Segments[i].Source < fx.Segments[j].Source
		}
		return fx.Segments[i].File < fx.Segments[j].File
	})
	for _, src := range []string{"main", "backup"} {
		hlsPath := src + "/video/index.m3u8"
		if err := writeFile(root, hlsPath, []byte(renderDemoHLS(src, fx))); err != nil {
			return nil, err
		}
		dashPath := src + "/index.mpd"
		if err := writeFile(root, dashPath, []byte(renderDemoDASH(src, fx))); err != nil {
			return nil, err
		}
		fx.HLSFiles = append(fx.HLSFiles, hlsPath)
		fx.DASHFiles = append(fx.DASHFiles, dashPath)
	}
	fx.Mappings = []string{
		"main track 1 -> video-avc1",
		"backup track 1 -> video-avc1",
	}
	return fx, nil
}

// demoTrackSegments returns one source's segments for a track, in file order.
func demoTrackSegments(source, track string, fx *DemoFixture) []DemoSegment {
	var out []DemoSegment
	for _, s := range fx.Segments {
		if s.Source == source && s.Track == track {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// renderDemoHLS writes the media playlist for one rendition of one source. URIs
// are relative to the rendition directory, as a real HLS packager emits.
func renderDemoHLS(source string, fx *DemoFixture) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	b.WriteString("#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
	fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%s/init.mp4\"\n", source)
	segs := demoTrackSegments(source, "video", fx)
	for i, seg := range segs {
		if i > 0 && seg.Start != segs[i-1].Start+segs[i-1].Dur {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n%s\n", float64(seg.Dur)/float64(BaseTimescale), basename(seg.File))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// renderDemoDASH writes a static MPD with SegmentTemplate+SegmentTimeline. The
// timeline entries are manifest-declared segment starts and durations, so the
// engine genuinely exercises manifest parsing rather than trusting media boxes.
func renderDemoDASH(source string, fx *DemoFixture) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	segs := demoTrackSegments(source, "video", fx)
	total := Ticks(0)
	for _, s := range segs {
		if s.Start+s.Dur > total {
			total = s.Start + s.Dur
		}
	}
	fmt.Fprintf(&b, `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT%.3fS">`+"\n",
		float64(total)/float64(BaseTimescale))
	b.WriteString("  <Period id=\"p0\" start=\"PT0S\">\n")
	b.WriteString("    <AdaptationSet contentType=\"video\" mimeType=\"video/mp4\" codecs=\"avc1.42C01F\" width=\"1280\" height=\"720\">\n")
	fmt.Fprintf(&b, "      <Representation id=%q bandwidth=\"1\">\n", source+"-video")
	fmt.Fprintf(&b, "        <SegmentTemplate timescale=\"%d\" startNumber=\"0\" initialization=\"%s/init.mp4\" media=\"%s/video/seg-$Number%%05d$.m4s\">\n",
		BaseTimescale, source, source)
	b.WriteString("          <SegmentTimeline>\n")
	for _, seg := range segs {
		fmt.Fprintf(&b, "            <S t=\"%d\" d=\"%d\"/>\n", seg.Start, seg.Dur)
	}
	b.WriteString("          </SegmentTimeline>\n        </SegmentTemplate>\n      </Representation>\n    </AdaptationSet>\n")
	b.WriteString("  </Period>\n</MPD>\n")
	return b.String()
}

func basename(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func writeFile(root, rel string, data []byte) error {
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return WrapError(err, KindCheckpoint, Location{File: rel, Offset: -1, Sample: -1}, "cannot create demo directory")
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return WrapError(err, KindCheckpoint, Location{File: rel, Offset: -1, Sample: -1}, "cannot write demo fixture")
	}
	return nil
}
