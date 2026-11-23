package app

import (
	"os"

	"github.com/zld/media-check/media"
	"github.com/zld/media-check/pipeline"
	"github.com/zld/media-check/transport"
)

func mergeRuntimeStats(s transport.Stats, m *pipeline.Manifest) transport.Stats {
	if m.RTP != nil {
		s.Duplicates = m.RTP.Duplicates
		s.Reordered = m.RTP.Reordered
		s.Lost = m.RTP.Lost
	}
	return s
}

func hashPath(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", media.WrapError(media.ClassIO, "hash.open", 0, err, "open %q", path)
	}
	defer f.Close()
	return media.HashFile(f)
}

// demoTargets is the fixed target layout: video first, then English audio.
func demoTargets() []media.TrackTarget {
	return []media.TrackTarget{
		{Kind: media.KindVideo, Required: true},
		{Kind: media.KindAudio, Required: true},
	}
}

// buildDemoFixture generates the MOV source used by the demo. Two tracks,
// stss keyframes, a ctts table and an edit list exercise the full parse path.
func buildDemoFixture(seed int64) (*media.Fixture, error) {
	return media.BuildFixture(media.FixtureOptions{
		Brand:   "qt  ",
		MovieTS: 1000,
		Seed:    seed,
		Tracks: []media.FixtureTrack{
			{ID: 1, Codec: "avc1", Lang: "eng", Timescale: 90000, Samples: 24, SyncEvery: 3, Width: 1280, Height: 720},
			{ID: 2, Codec: "mp4a", Lang: "eng", Timescale: 48000, Samples: 24, SyncEvery: 0},
		},
	})
}

// targetOrderedTracks maps a remuxed probe's tracks into a stable order by id.
func targetOrderedTracks(p *media.Probe, res *media.RemuxResult) []*media.TrackInfo {
	var out []*media.TrackInfo
	for _, ev := range res.Tracks {
		for _, ti := range p.Tracks {
			if ti.TrackID == ev.TargetID {
				out = append(out, ti)
			}
		}
	}
	return out
}
