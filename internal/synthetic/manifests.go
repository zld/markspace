package synthetic

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// writeManifests emits one HLS media playlist (v1 video rendition) and one
// DASH MPD covering both video renditions and the audio track.
func writeManifests(cfg FixtureConfig, root string, p *FixturePaths) error {
	pk := cfg.ProgramKey
	hlsPath := filepath.Join(root, "hls", pk, "video.m3u8")
	p.HLSManifest = hlsPath
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:1\n#EXT-X-MEDIA-SEQUENCE:0\n")
	b.WriteString(`#EXT-X-MAP:URI="v1/init.mp4"` + "\n")
	for i := 0; i < cfg.Segments; i++ {
		fmt.Fprintf(&b, "#EXTINF:1.0,\nv1/seg%d.m4s\n", i)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	if err := os.WriteFile(hlsPath, []byte(b.String()), 0o644); err != nil {
		return err
	}

	dashPath := filepath.Join(root, "dash", pk, "manifest.mpd")
	p.DASHManifest = dashPath
	if err := os.MkdirAll(filepath.Dir(dashPath), 0o755); err != nil {
		return err
	}
	// Media lives under dash/<pk>/media so all URIs stay within the manifest
	// directory (no ".." traversal, per the SafeURI contract).
	mediaRoot := filepath.Join(root, "dash", pk, "media")
	for _, sub := range []string{"v1", "v2", "audio"} {
		if err := os.MkdirAll(filepath.Join(mediaRoot, sub), 0o755); err != nil {
			return err
		}
	}
	hlsRoot := filepath.Join(root, "hls", pk)
	for _, sub := range []string{"v1", "v2", "audio"} {
		files, err := os.ReadDir(filepath.Join(hlsRoot, sub))
		if err != nil {
			return err
		}
		for _, f := range files {
			data, err := os.ReadFile(filepath.Join(hlsRoot, sub, f.Name()))
			if err != nil {
				return err
			}
			name := f.Name()
			if seg, ok := hlsSegNumber(name); ok {
				// DASH startNumber=1 while HLS segments are zero-based.
				name = fmt.Sprintf("%d.m4s", seg+1)
			}
			if err := os.WriteFile(filepath.Join(mediaRoot, sub, name), data, 0o644); err != nil {
				return err
			}
		}
	}
	mpd := buildMPD(cfg)
	return os.WriteFile(dashPath, []byte(mpd), 0o644)
}

func buildMPD(cfg FixtureConfig) string {
	var b strings.Builder
	b.WriteString(`<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static">` + "\n")
	b.WriteString(`  <Period id="0">` + "\n")
	writeAdaptation(&b, "video", 90000, "media/v2/$Number$.m4s",
		"media/v2/init.mp4", cfg.Segments, 90000)
	writeAdaptation(&b, "audio", 48000, "media/audio/$Number$.m4s",
		"media/audio/init.mp4", cfg.Segments, 48000)
	b.WriteString("  </Period>\n</MPD>\n")
	return b.String()
}

func writeAdaptation(b *strings.Builder, kind string, ts uint64, media, init string, n int, d int64) {	fmt.Fprintf(b, `    <AdaptationSet contentType="%s">`+"\n", kind)
	if init != "" {
		fmt.Fprintf(b, `      <SegmentTemplate timescale="%d" initialization="%s" media="%s" startNumber="1">`+"\n",
			ts, init, media)
	} else {
		fmt.Fprintf(b, `      <SegmentTemplate timescale="%d" media="%s" startNumber="1">`+"\n", ts, media)
	}
	b.WriteString("        <SegmentTimeline>\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(b, `          <S t="%d" d="%d"/>`+"\n", int64(i)*d, d)
	}
	b.WriteString("        </SegmentTimeline>\n      </SegmentTemplate>\n    </AdaptationSet>\n")
}

// hlsSegNumber extracts the zero-based index from names like "seg7.m4s".
func hlsSegNumber(name string) (int, bool) {
	if !strings.HasPrefix(name, "seg") || !strings.HasSuffix(name, ".m4s") {
		return 0, false
	}
	num := strings.TrimSuffix(strings.TrimPrefix(name, "seg"), ".m4s")
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, false
	}
	return n, true
}

func writeRTP(rng *rand.Rand, root string, cfg FixtureConfig, p *FixturePaths) error {
	// 50 packets starting near both moduli to exercise seq+ts wrap-around.
	cap := BuildRTPCapture(rng, 0x4d544c31, 50, true)
	// In-window reordering and a duplicate packet.
	cap.Reorder(5, 6)
	cap.Duplicate(8)
	raw, err := cap.Encode()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "rtp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p.RTPFile = filepath.Join(dir, "audio.rtp")
	return os.WriteFile(p.RTPFile, raw, 0o644)
}

func writeEvents(root string, cfg FixtureConfig, p *FixturePaths) error {
	rng := rand.New(rand.NewSource(cfg.Seed + 1))
	evs := BuildEvents(rng, EventConfig{
		Count: cfg.Segments, RatePPM: cfg.DriftRatePPM, OffsetNS: cfg.DriftOffsetNS,
		LateIndices: cfg.LateEvents,
	})
	raw, err := EncodeEvents(evs)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p.EventFile = filepath.Join(dir, "client.ndjson")
	return os.WriteFile(p.EventFile, raw, 0o644)
}
