package pipeline

import (
	"os"
	"path/filepath"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/journal"
	"mediarun/internal/manifest"
)

// buildTimeline parses every manifest and maps all segments onto the
// unified timeline (manifest stage).
func buildTimeline(inDir string, manifests []string) (*Timeline, error) {
	tl := &Timeline{}
	for _, name := range manifests {
		path := filepath.Join(inDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fault.New(fault.StageInput, "cannot read manifest").At(path, -1).Wrap(err)
		}
		tl.Manifests = append(tl.Manifests, FileDigest{Path: name, SHA256: journal.DigestBytes(data)})
		switch filepath.Ext(name) {
		case ".mpd":
			mpd, err := manifest.ParseMPD(path, data)
			if err != nil {
				return nil, err
			}
			for _, rep := range mpd.Representations {
				rt := RepTimeline{
					ID: rep.ID, Bandwidth: rep.Bandwidth,
					Timescale: rep.Timescale, Init: rep.Init,
				}
				var cursor clock.Ticks
				for _, s := range rep.Segments {
					rt.Segments = append(rt.Segments, SegTimeline{
						Media: s.Media, Number: s.Number,
						DeclaredStart: cursor, Duration: s.Duration, MPDOffset: s.Offset,
					})
					cursor += s.Duration
				}
				tl.Reps = append(tl.Reps, rt)
			}
		case ".m3u8":
			pl, err := manifest.ParsePlaylist(path, data)
			if err != nil {
				return nil, err
			}
			ht := HLSTimeline{Playlist: name}
			for _, s := range pl.Segments {
				ht.Segments = append(ht.Segments, HLSSegTimeline{
					URI: s.URI, DeclaredStart: s.Start, Duration: s.Duration,
					DiscSeq: s.DiscSeq, Offset: s.Offset,
				})
			}
			tl.HLS = append(tl.HLS, ht)
		}
	}
	return tl, nil
}
