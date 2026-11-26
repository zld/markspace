package fixture

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"cmafrepair/pkg/media/timebase"
)

// Datasets is the ordered list produced by Generate.
var Datasets = []string{DSClean, DSEdge, DSFaults}

// Summary describes what a Generate call wrote.
type Summary struct {
	Root            string
	Seed            int64
	Representations int
	FragmentsPerRep int
	Datasets        []string
}

// Generate writes every dataset under root. The same seed always yields
// byte-identical files (deterministic mdat fill), so repeated runs are
// reproducible and cache behavior is testable.
func Generate(root string, seed int64) (Summary, error) {
	sum := Summary{
		Root: root, Seed: seed, Representations: len(Reps),
		FragmentsPerRep: FragmentsPerRep, Datasets: Datasets,
	}
	for _, ds := range Datasets {
		if err := generateDataset(root, ds, seed); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

func generateDataset(root, dataset string, seed int64) error {
	for ri, r := range Reps {
		dir := filepath.Join(root, dataset, r.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return timebase.NewError(timebase.CodeIO, timebase.PhaseManifest,
				"mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "init.mp4"),
			initSegment(r.Timescale, r.Duration), 0o644); err != nil {
			return timebase.NewError(timebase.CodeIO, timebase.PhaseManifest, "write init: %v", err)
		}
		frags := repFragments(dataset, ri, r)
		for i, data := range frags {
			fillDeterministic(data, seed, ri, i)
			name := fmt.Sprintf("frag_%04d.m4s", i+1)
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				return timebase.NewError(timebase.CodeIO, timebase.PhaseManifest,
					"write %s: %v", name, err)
			}
		}
	}
	anchors := buildAnchors(dataset)
	ab, err := marshalAnchors(anchors)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, dataset, "anchors.json"), ab, 0o644); err != nil {
		return timebase.NewError(timebase.CodeIO, timebase.PhaseManifest,
			"write anchors: %v", err)
	}
	return nil
}

// fillDeterministic writes a seed-derived byte pattern into the mdat payload
// of a fragment so distinct seeds (and distinct fragments) hash differently,
// while identical seeds reproduce identical bytes.
func fillDeterministic(data []byte, seed int64, repIdx, fragIdx int) {
	mdatPayload := findMdatPayload(data)
	if mdatPayload < 0 {
		return
	}
	for i := mdatPayload; i < len(data); i++ {
		v := uint64(seed)*1099511628211 +
			uint64(repIdx)*1000003 +
			uint64(fragIdx)*97 +
			uint64(i-mdatPayload)
		data[i] = byte(v >> 24)
	}
}

// findMdatPayload locates the mdat box and returns its payload offset.
func findMdatPayload(data []byte) int {
	pos := 0
	for pos+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		typ := string(data[pos+4 : pos+8])
		if size < 8 || pos+size > len(data) {
			return -1
		}
		if typ == "mdat" {
			return pos + 8
		}
		pos += size
	}
	return -1
}

// LoadAnchors reads an anchors.json sidecar.
func LoadAnchors(path string) (AnchorFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return AnchorFile{}, timebase.NewError(timebase.CodeIO, timebase.PhaseClock,
			"read anchors %s: %v", path, err)
	}
	var af AnchorFile
	if err := unmarshalAnchors(b, &af); err != nil {
		return AnchorFile{}, err
	}
	return af, nil
}
