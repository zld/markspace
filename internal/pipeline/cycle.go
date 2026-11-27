package pipeline

import (
	"encoding/json"
	"os"

	"medtrace/internal/fixture"
	"medtrace/internal/mederr"
	"medtrace/internal/track"
)

// CycleSpec is the on-disk dependency-cycle descriptor (cycle.json).
type CycleSpec struct {
	Tracks []fixture.CycleTrackSpec `json:"tracks"`
}

// LoadCycleSpec reads a cycle descriptor; a missing file returns (nil, nil).
func LoadCycleSpec(path string) (*CycleSpec, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, mederr.New(mederr.CatIO, "read cycle spec %q", path).Wrap(err)
	}
	var spec CycleSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, mederr.New(mederr.CatInvalidInput, "cycle spec is not valid JSON").Wrap(err)
	}
	return &spec, nil
}

// VerifyCycle builds the dependency graph from a cycle descriptor and returns
// the dependency_cycle error when a ring is present. The independent track
// must still be representable in the graph.
func VerifyCycle(spec *CycleSpec) ([]uint64, error) {
	tracks2 := make([]track.Track, 0, len(spec.Tracks))
	for _, t := range spec.Tracks {
		tracks2 = append(tracks2, track.Track{
			ID:        t.ID,
			Kind:      track.Kind(t.Kind),
			CodecID:   t.Codec,
			DependsOn: append([]uint64(nil), t.DependsOn...),
		})
	}
	g, err := track.NewGraph(tracks2)
	if err != nil {
		return nil, err
	}
	return g.TopoSort()
}
