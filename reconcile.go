package mediacore

// applyMetadata runs audio and color arbitration once per logical track.
func applyMetadata(playlists []*Playlist, items []*Item) ([]*Error, []ColorResolution) {
	var fails []*Error
	var colors []ColorResolution
	type cand struct {
		c AudioCandidate
		t *TrackDecl
	}
	byTrack := map[uint32][]cand{}
	colorByTrack := map[uint32][]ColorCandidate{}
	for _, pl := range playlists {
		for _, tr := range pl.Tracks {
			if tr.Kind == "audio" && tr.Rate != 0 {
				byTrack[tr.ID] = append(byTrack[tr.ID], cand{
					c: AudioCandidate{SourceMeta: SourceMeta{Asset: pl.AssetID, Version: tr.Version, Track: tr.ID},
						Rate: tr.Rate, Layout: tr.Layout}, t: tr})
			}
			// Color candidates come from the parsed segment views of this track.
			for _, it := range itemsOfTrack(items, tr.ID, pl.AssetID, tr.Version) {
				if it.Info != nil && it.Info.Color.Present {
					colorByTrack[tr.ID] = append(colorByTrack[tr.ID], ColorCandidate{
						SourceMeta: SourceMeta{Asset: pl.AssetID, Version: tr.Version, Track: tr.ID},
						Prio:       tr.ColorPrio, Color: it.Info.Color})
				}
			}
		}
	}
	for _, cs := range byTrack {
		cands := make([]AudioCandidate, len(cs))
		explicit := false
		var target AudioSpec
		for i, x := range cs {
			cands[i] = x.c
			if x.t.TargetRate != 0 && x.t.Target != "" {
				explicit = true
				target = AudioSpec{Rate: x.t.TargetRate, Layout: x.t.Target}
			}
		}
		_, rejects := ResolveAudio(assetOfAudio(cands), cands, target, explicit)
		if len(rejects) > 0 {
			fails = append(fails, rejects...)
		}
	}
	for _, cands := range colorByTrack {
		colors = append(colors, ResolveColor(cands))
	}
	return fails, colors
}

func itemsOfTrack(items []*Item, track uint32, asset, version string) []*Item {
	var out []*Item
	for _, it := range items {
		if it.TrackID == track && it.Asset == asset && it.Version == version {
			out = append(out, it)
		}
	}
	return out
}

func assetOfAudio(cs []AudioCandidate) string {
	if len(cs) == 0 {
		return ""
	}
	return cs[0].Asset
}

// chargeBudget applies the deterministic per-item cost. The value is fully
// reproducible and is compared against the configured time budget; wall-clock
// noise is deliberately excluded so repeat runs summarize identically.
func chargeBudget(items []*Item, perItem int64, b *Budget) {
	b.ActualElapsedMS = int64(len(items)) * perItem
	if b.TimeBudgetMS > 0 && b.ActualElapsedMS > b.TimeBudgetMS {
		b.Exceeded = true
	}
}
