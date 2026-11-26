package fixtures

import (
	"fmt"
	"os"
	"path/filepath"

	"rtx/internal/cmaf"
)

// The scenario builders each produce one dataset. They share a common shape so
// the acceptance commands can predict what each must demonstrate:
//
//	good       clean single source, no anomalies
//	edge       two sources: mfhd gap + tfdt rollback + source switch, plus
//	           RTP loss, reordering and a 32-bit timestamp wrap
//	gap        mfhd gap only
//	rollback   tfdt rollback only
//	switch     source switch only
//	truncated  a fragment cut mid-mdat
//	badcache   good media with a poisoned cache entry written alongside
//	oversize   a fragment above 64 MiB
//	queue      more than 32 fragment records
//	nomfhd     one fragment has no mfhd box; no sequence or duration is inferred

func buildGood(workDir string, opts Options) (Scenario, error) {
	src := noMutation()
	src.id, src.ssrc, src.tsBase = "cam-a", 0x1001, 1_000_000
	return generateDataset(workDir, opts, NameGood, []sourcePlan{src}, nil, false)
}

func buildGapOnly(workDir string, opts Options) (Scenario, error) {
	src := noMutation()
	src.id, src.ssrc, src.tsBase = "cam-a", 0x1001, 1_000_000
	src.seqGapAt = 1
	return generateDataset(workDir, opts, NameGap, []sourcePlan{src}, nil, false)
}

func buildRollbackOnly(workDir string, opts Options) (Scenario, error) {
	src := noMutation()
	src.id, src.ssrc, src.tsBase = "cam-a", 0x1001, 1_000_000
	src.rollbackAt = 2
	return generateDataset(workDir, opts, NameRollback, []sourcePlan{src}, nil, false)
}

func buildSwitchOnly(workDir string, opts Options) (Scenario, error) {
	a := noMutation()
	a.id, a.ssrc, a.tsBase = "cam-a", 0x1001, 1_000_000
	b := noMutation()
	b.id, b.ssrc, b.tsBase = "cam-b", 0x2002, 3_500_000
	// The second source continues where the first ends: 4 fragments x 3
	// frames x 3600 video ticks = 43200 ticks = 345600 media ticks.
	b.mediaBase = 345600
	events := []cmaf.Event{{
		Kind: cmaf.EventSourceSwitch, Source: "cam-b", Note: "cam-a -> cam-b",
		MediaTicks: b.mediaBase,
	}}
	return generateDataset(workDir, opts, NameSwitch, []sourcePlan{a, b}, events, false)
}

// buildEdge combines all three media anomalies and exercises the RTP pipeline
// with loss, reordering and a 32-bit timestamp wrap.
func buildEdge(workDir string, opts Options) (Scenario, error) {
	a := noMutation()
	a.id, a.ssrc = "cam-a", 0x1001
	// Start near the top of the 32-bit RTP space so the trace crosses the wrap.
	a.tsBase = 4294967296 - 8000
	a.seqGapAt = 1
	a.rollbackAt = 2

	b := noMutation()
	b.id, b.ssrc = "cam-b", 0x2002
	b.tsBase = 3_500_000
	b.mediaBase = 345600

	events := []cmaf.Event{
		{Kind: cmaf.EventSourceSwitch, Source: "cam-b", Note: "cam-a -> cam-b", MediaTicks: b.mediaBase},
	}
	sc, err := generateDataset(workDir, opts, NameEdge, []sourcePlan{a, b}, events, false)
	if err != nil {
		return Scenario{}, err
	}
	if err := disturbTrace(filepath.Join(sc.Dir, "cam-a.rtp.jsonl")); err != nil {
		return Scenario{}, err
	}
	return sc, nil
}

func buildTruncated(workDir string, opts Options) (Scenario, error) {
	src := noMutation()
	src.id, src.ssrc, src.tsBase = "cam-a", 0x1001, 1_000_000
	return generateDataset(workDir, opts, NameTruncated, []sourcePlan{src}, nil, true)
}

// buildBadCache writes good media plus a poisoned cache marker file that the
// cache tests point at. The marker itself is inert data; the test corrupts a
// real cache directory entry.
func buildBadCache(workDir string, opts Options) (Scenario, error) {
	src := noMutation()
	src.id, src.ssrc, src.tsBase = "cam-a", 0x1001, 1_000_000
	sc, err := generateDataset(workDir, opts, NameBadCache, []sourcePlan{src}, nil, false)
	if err != nil {
		return Scenario{}, err
	}
	note := []byte("the cache test seeds this directory's cache, corrupts one payload, and expects a recomputation\n")
	return sc, writeAtomic(filepath.Join(sc.Dir, "CACHE_TEST.txt"), note)
}

// buildOversize emits a single fragment header claiming more than 64 MiB. The
// payload is not actually written, keeping the fixture tree small; the parser
// rejects it on the declared size before reading.
func buildOversize(workDir string, opts Options) (Scenario, error) {
	sc, err := generateDataset(workDir, opts, NameOversize, []sourcePlan{noMutationWithID("cam-a", 0x1001)}, nil, false)
	if err != nil {
		return Scenario{}, err
	}
	m, err := cmaf.LoadManifest(sc.Manifest)
	if err != nil {
		return Scenario{}, err
	}
	// Rebuild the manifest to point at one oversized file.
	big := make([]byte, 16)
	putUint32(big[0:], 200<<20) // declared 200 MiB
	copy(big[4:8], "mdat")
	bigName := "cam-a-9999.m4s"
	if err := writeAtomic(filepath.Join(sc.Dir, bigName), big); err != nil {
		return Scenario{}, err
	}
	m.Sources[0].Fragments = append(m.Sources[0].Fragments, cmaf.FragmentRef{Path: bigName, Order: 99})
	if err := cmaf.WriteManifestFile(sc.Manifest, m); err != nil {
		return Scenario{}, err
	}
	return sc, nil
}

// buildQueueOverflow emits more fragment records than the 32-record queue.
func buildQueueOverflow(workDir string, opts Options) (Scenario, error) {
	// Empty stub fragments are not valid CMAF, so re-use a single valid
	// fragment's bytes under many names: the run rejects on record count before
	// parsing any of them.
	src := noMutationWithID("cam-a", 0x1001)
	sc, err := generateDataset(workDir, opts, NameQueue, []sourcePlan{src}, nil, false)
	if err != nil {
		return Scenario{}, err
	}
	m, err := cmaf.LoadManifest(sc.Manifest)
	if err != nil {
		return Scenario{}, err
	}
	first := m.Sources[0].Fragments[0].Path
	data, err := os.ReadFile(filepath.Join(sc.Dir, first))
	if err != nil {
		return Scenario{}, err
	}
	for i := len(m.Sources[0].Fragments); i < 40; i++ {
		name := fmt.Sprintf("cam-a-%04d.m4s", i)
		if err := writeAtomic(filepath.Join(sc.Dir, name), data); err != nil {
			return Scenario{}, err
		}
		m.Sources[0].Fragments = append(m.Sources[0].Fragments, cmaf.FragmentRef{Path: name, Order: i + 1})
	}
	return sc, cmaf.WriteManifestFile(sc.Manifest, m)
}

// buildNoMFHD omits the mfhd box from the second fragment. The run must record
// no invented sequence number or duration for it.
func buildNoMFHD(workDir string, opts Options) (Scenario, error) {
	src := noMutation()
	src.id, src.ssrc, src.tsBase = "cam-a", 0x1001, 1_000_000
	src.omitMFHDAt = 1
	return generateDataset(workDir, opts, NameNoMFHD, []sourcePlan{src}, nil, false)
}

func noMutationWithID(id string, ssrc uint32) sourcePlan {
	src := noMutation()
	src.id, src.ssrc, src.tsBase = id, ssrc, 1_000_000
	return src
}

func putUint32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}
