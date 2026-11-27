package pipeline

import (
	"os"
	"path/filepath"
	"strings"

	"mediatimeline/asset"
	"mediatimeline/internal/synthetic"
	"mediatimeline/manifest"
)

// lateEventIndex maps the --late-event nanosecond hint to the closest event
// index. Events are 1s apart starting at 500ms, so index = round((ns-5e8)/1e9).
func lateEventIndex(cfg Config) []int {
	if cfg.LateEventNS <= 0 {
		return nil
	}
	idx := int((cfg.LateEventNS - 500_000_000 + 500_000_000) / 1_000_000_000)
	if idx < 0 {
		idx = 0
	}
	if idx > 8 {
		idx = 8
	}
	return []int{idx}
}

// registerFragments streams every manifest segment through the MP4 parser and
// registers surviving versions in the asset map. A broken segment is recorded
// with its byte offset but never stops later, independent intervals.
func registerFragments(hls, dash *manifest.Manifest, paths *synthetic.FixturePaths,
	am *asset.Map, fail func(Failure)) int64 {
	var truncOffset int64 = -1
	priorityOf := func(uri string) int {
		// Explicit priority rule: v2 renditions outrank v1 on conflicts.
		if strings.Contains(uri, "/v2/") {
			return 10
		}
		return 0
	}
	register := func(m *manifest.Manifest, source string) {
		for _, track := range m.Tracks {
			for _, seg := range track.Segments {
				absURI := filepath.Join(m.BaseDir, seg.URI)
				relURI := strings.TrimPrefix(absURI, paths.Root+string(filepath.Separator))
				f, err := os.Open(absURI)
				if err != nil {
					fail(Failure{Code: "ASSET_OPEN_FAILED", Stage: StageFragment,
						Asset: relURI, Track: track.ID, Message: err.Error()})
					continue
				}
				frag, perr := asset.ParseFragment(f, track.Timescale)
				f.Close()
				if perr != nil {
					off := extractByteOffset(perr)
					if strings.Contains(perr.Error(), "truncated") ||
						strings.Contains(perr.Error(), "fragment parse") {
						truncOffset = off
					}
					fail(Failure{Code: "ASSET_TRUNCATED", Stage: StageFragment,
						Asset: relURI, Track: track.ID, ByteOffset: off, Message: perr.Error()})
					continue
				}
				am.Register(asset.Version{
					ProgramKey: m.ProgramKey, TrackID: track.Kind,
					Interval: asset.Interval{StartNS: seg.StartNS, EndNS: seg.StartNS + seg.DurationNS},
					Source:   source + ":" + relURI, Digest: frag.ContentSHA256,
					Priority: priorityOf(relURI), Sequence: seg.Sequence, Frames: len(frag.Samples),
				})
			}
		}
	}
	register(hls, "hls")
	register(dash, "dash")
	return truncOffset
}

// extractByteOffset pulls the byte position embedded by asset.ParseFragment.
func extractByteOffset(err error) int64 {
	msg := err.Error()
	i := strings.Index(msg, "byte ")
	if i < 0 {
		return 0
	}
	rest := strings.TrimPrefix(msg[i:], "byte ")
	var n int64
	for _, c := range rest {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
