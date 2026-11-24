package pipeline

import (
	"encoding/json"
	"fmt"

	"archive/internal/manifest"
	"archive/internal/render"
	"archive/internal/rtp"
	"archive/internal/segment"
)

// LayerStat is the per-representation view reported in a scenario.
type LayerStat struct {
	Name            string  `json:"name"`
	TargetKbps      int     `json:"target_kbps"`
	ActualKbps      float64 `json:"actual_kbps"`
	Frames          int     `json:"frames"`
	Delivered       int     `json:"delivered_packets"`
	Packets         int     `json:"packets"`
	Duplicates      int     `json:"duplicates"`
	Reordered       int     `json:"reordered"`
	Lost            int     `json:"lost"`
	Gaps            int     `json:"gaps"`
	PSNRDb          float64 `json:"psnr_db"`
	StartNS         int64   `json:"start_ns"`
	EndNS           int64   `json:"end_ns"`
	Isolated        bool    `json:"isolated"`
	IsolationReason string  `json:"isolation_reason,omitempty"`
}

// runScenario transports every geometry independently and fans them in.
func runScenario(mode string, ref trackView, frameCount int, fm feedMode, lossBurst int) (Scenario, error) {
	geoms := defaultGeometries()
	sc := Scenario{Mode: mode, LayerCount: len(geoms), RecoveredLayers: []string{}, IsolatedLayers: []string{}}
	seed := render.Seed(ref.ID)
	res := ref.Resolution
	expectedEnd := int64(frameCount) * FrameInterval
	sessionSeconds := float64(expectedEnd) / 1e9

	var inputs []segment.LayerInput
	var rl []*recoveredLayer
	for _, g := range geoms {
		lp, err := buildLayerPlane(g, res, seed, frameCount)
		if err != nil {
			return sc, err
		}
		rec, err := transportLayer(lp, frameCount, fm, lossBurst)
		if err != nil {
			return sc, err
		}
		rl = append(rl, rec)
		inputs = append(inputs, segment.LayerInput{
			Layer:           rtp.Layer{Name: g.name, TargetKbps: g.kbps, FrameIntervalNS: FrameInterval, TSStep: rtp.TSOffset},
			Result:          rec.rec,
			StartNS:         rec.startNS,
			EndNS:           rec.endNS,
			FrameIntervalNS: FrameInterval,
			ExpectedStartNS: 0,
			ExpectedEndNS:   expectedEnd,
		})
		sc.Duplicates += rec.rec.Duplicates
		sc.Reordered += rec.rec.Reordered
		sc.Lost += rec.rec.Lost
		sc.Gaps += len(rec.rec.Gaps)
		sc.Log = append(sc.Log, fmt.Sprintf("layer %s packets=%d delivered=%d dups=%d reordered=%d lost=%d gaps=%d",
			g.name, rec.packets, len(rec.rec.Ordered), rec.rec.Duplicates, rec.rec.Reordered, rec.rec.Lost, len(rec.rec.Gaps)))
		for _, gap := range rec.rec.Gaps {
			sc.Log = append(sc.Log, fmt.Sprintf("layer %s gap [%d..%d] count=%d recoverable=%t reason=%s",
				g.name, gap.Start, gap.End, gap.Count, gap.Recoverable, gap.Reason))
		}
	}

	summary, fanErr := segment.FanIn(inputs, segment.MaxBoundaryErrorNS)
	verdict := make(map[string]segment.LayerStatus)
	if fanErr != nil {
		sc.Log = append(sc.Log, "fan-in failed: "+fanErr.Error())
		for _, in := range inputs {
			verdict[in.Layer.Name] = segment.LayerStatus{Name: in.Layer.Name, Isolated: true, Reason: fanErr.Error()}
			sc.IsolatedLayers = append(sc.IsolatedLayers, in.Layer.Name)
		}
	} else {
		sc.ManifestAlignNS = summary.ManifestAlignNS
		for _, st := range summary.Layers {
			verdict[st.Name] = st
			if st.Isolated {
				sc.IsolatedLayers = append(sc.IsolatedLayers, st.Name)
				sc.Log = append(sc.Log, fmt.Sprintf("isolated %s: %s", st.Name, st.Reason))
				continue
			}
			sc.RecoveredLayers = append(sc.RecoveredLayers, st.Name)
			sc.ActiveLayers++
			sc.Log = append(sc.Log, fmt.Sprintf("recovered %s frames=%d boundary_err=%d ns", st.Name, st.Frames, st.MaxBoundaryErrNS))
		}
	}

	var targetSum int
	var actualSum float64
	var psnrSum float64
	var psnrN int
	for _, rec := range rl {
		st := verdict[rec.geom.name]
		var bytes int
		for _, p := range rec.rec.Ordered {
			bytes += len(p.Payload)
		}
		actual := 0.0
		if sessionSeconds > 0 {
			actual = float64(bytes*8) / sessionSeconds / 1000
		}
		ls := LayerStat{
			Name:            rec.geom.name,
			TargetKbps:      rec.geom.kbps,
			ActualKbps:      actual,
			Frames:          st.Frames,
			Delivered:       len(rec.rec.Ordered),
			Packets:         rec.packets,
			Duplicates:      rec.rec.Duplicates,
			Reordered:       rec.rec.Reordered,
			Lost:            rec.rec.Lost,
			Gaps:            len(rec.rec.Gaps),
			PSNRDb:          rec.psnr,
			StartNS:         rec.startNS,
			EndNS:           rec.endNS,
			Isolated:        st.Isolated,
			IsolationReason: st.Reason,
		}
		sc.Layers = append(sc.Layers, ls)
		if st.Isolated {
			continue
		}
		targetSum += ls.TargetKbps
		actualSum += ls.ActualKbps
		psnrSum += ls.PSNRDb
		psnrN++
	}
	sc.TargetBitrateKbps = targetSum
	sc.ActualBitrateKbps = actualSum
	sc.BitrateDeviationPct = deviation(actualSum, targetSum)
	if psnrN > 0 {
		sc.QualityPSNRDb = psnrSum / float64(psnrN)
	}
	return sc, nil
}

func deviation(actual float64, target int) float64 {
	if target <= 0 {
		return 0
	}
	d := (actual - float64(target)) / float64(target) * 100
	if d < 0 {
		d = -d
	}
	return d
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// layerManifestEntries projects the normal scenario into the frame manifest.
func layerManifestEntries(normal Scenario) []manifest.LayerEntry {
	out := make([]manifest.LayerEntry, 0, len(normal.Layers))
	for _, ls := range normal.Layers {
		out = append(out, manifest.LayerEntry{
			Name:         ls.Name,
			TargetKbps:   ls.TargetKbps,
			ActualKbps:   ls.ActualKbps,
			DeviationPct: deviation(ls.ActualKbps, ls.TargetKbps),
			Frames:       ls.Frames,
			Duplicates:   ls.Duplicates,
			Reordered:    ls.Reordered,
			Lost:         ls.Lost,
			Gaps:         ls.Gaps,
			PSNRDb:       ls.PSNRDb,
			Isolated:     ls.Isolated,
			Reason:       ls.IsolationReason,
		})
	}
	return out
}

func jsonMarshal(v interface{}) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
