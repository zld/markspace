package main

import (
	"bytes"
	"fmt"
	"path/filepath"

	"mediacheck/internal/asset"
	"mediacheck/internal/budget"
	"mediacheck/internal/cache"
	"mediacheck/internal/derive"
	"mediacheck/internal/metrics"
	"mediacheck/internal/track"
)

type cachedFragment struct {
	node   cache.Node
	hit    bool
	data   []byte
	sha    string
	target int64
	actual float64
	snr    float64
	align  int
}

func buildCachedFragment(st *cache.Store, g *cache.Graph, dir, id, trackID, sourceID string, f *pcmFixture, bits int) (*cachedFragment, error) {
	sourceSHA := asset.DigestBytes(f.WAV)
	configSHA := mustConfigDigest(map[string]any{
		"bits": bits, "frames": f.Frames, "rate": f.Rate, "channels": f.Ch, "version": 1,
	})
	node, err := g.Add(cache.Node{
		ID: id, TrackID: trackID, SourceSHA: sourceSHA, ConfigSHA: configSHA,
		DependsOn: []string{sourceID},
	})
	if err != nil {
		return nil, err
	}
	data, hit, err := st.Get(node.Key)
	if err != nil {
		return nil, err
	}
	if !hit {
		tr, err := track.ReadWAV(trackID, bytes.NewReader(f.WAV), int64(len(f.WAV)))
		if err != nil {
			return nil, err
		}
		segs, err := derive.BuildAll(tr, bits, int64(f.Frames))
		if err != nil {
			return nil, err
		}
		if len(segs) != 1 {
			return nil, fmt.Errorf("cache case expected one fragment, got %d", len(segs))
		}
		data = segs[0].Data
		if _, err := st.Put(node.Key, data); err != nil {
			return nil, err
		}
	}
	if err := cache.AtomicWriteFile(filepath.Join(dir, id+".m4s"), data, 0o644); err != nil {
		return nil, err
	}
	target, err := budget.TargetBPS(f.Rate, f.Ch, bits)
	if err != nil {
		return nil, err
	}
	payloadBytes := int64(f.Frames * f.Ch * bits / 8)
	actual, err := metrics.ActualBitrate(payloadBytes, int64(f.Frames), f.Rate)
	if err != nil {
		return nil, err
	}
	recon, err := metrics.QuantizeLeftAligned(f.Samples, bits)
	if err != nil {
		return nil, err
	}
	alignment := metrics.AlignAndSNR(f.Samples, recon, 1)
	return &cachedFragment{
		node: node, hit: hit, data: data, sha: asset.DigestBytes(data),
		target: target, actual: actual, snr: alignment.SNRDB, align: alignment.ErrorSamples,
	}, nil
}

func caseRepeat(out string) Report {
	dir := out
	rep := newReport("repeat", dir)
	st, err := cache.NewStore(filepath.Join(dir, "cache"))
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	g := cache.NewGraph()

	a1 := genTone(toneSpec{SampleRate: 48000, Channels: 2, Frames: 8192, Frequency: 440})
	b1 := genTone(toneSpec{SampleRate: 48000, Channels: 2, Frames: 8192, Frequency: 659.25})

	firstA, err := buildCachedFragment(st, g, dir, "frag-a", "track-A", "source-A", a1, 24)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	firstB, err := buildCachedFragment(st, g, dir, "frag-b", "track-B", "source-B", b1, 24)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	secondA, err := buildCachedFragment(st, g, dir, "frag-a", "track-A", "source-A", a1, 24)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	secondB, err := buildCachedFragment(st, g, dir, "frag-b", "track-B", "source-B", b1, 24)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	if !secondA.hit || !secondB.hit {
		rep.Error = fmt.Sprintf("identical rerun misses: A=%v B=%v", secondA.hit, secondB.hit)
		return rep
	}
	if firstA.node.Key != secondA.node.Key || asset.DigestBytes(a1.WAV) != secondA.node.SourceSHA {
		rep.Error = "repeated source digest or derivation key changed"
		return rep
	}

	affected := g.Affected("source-A")
	if len(affected) != 1 || affected[0] != "frag-a" {
		rep.Error = fmt.Sprintf("affected=%v want [frag-a]", affected)
		return rep
	}

	// A late RTP packet arrives and creates a new immutable source revision.
	a2 := genTone(toneSpec{SampleRate: 48000, Channels: 2, Frames: 8192, Frequency: 445})
	if asset.DigestBytes(a2.WAV) == asset.DigestBytes(a1.WAV) {
		rep.Error = "revised source did not change digest"
		return rep
	}
	revisedA, err := buildCachedFragment(st, g, dir, "frag-a", "track-A", "source-A", a2, 24)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	unaffectedB, err := buildCachedFragment(st, g, dir, "frag-b", "track-B", "source-B", b1, 24)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	if !unaffectedB.hit {
		rep.Error = "unaffected track missed cache after source A revision"
		return rep
	}

	delta, err := metrics.DeltaPercent(firstB.target, firstB.actual)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	rep.SourceSHA = secondA.node.SourceSHA
	rep.TargetBPS = firstB.target
	rep.ActualBPS = firstB.actual
	rep.DeltaPct = delta
	rep.SNRDB = firstB.snr
	rep.AlignErrSamples = firstB.align
	rep.Invalidated = len(affected)
	rep.CacheHit = 1
	rep.Fragments = 2
	rep.Segments = 2
	rep.OK = true
	rep.Details["identical_rerun_hits"] = 2
	rep.Details["unaffected_track"] = "track-B"
	rep.Details["unaffected_cache_hit"] = unaffectedB.hit
	rep.Details["revised_source_sha"] = revisedA.node.SourceSHA
	rep.Details["revised_key_changed"] = revisedA.node.Key != secondA.node.Key
	rep.Details["first_run_a_hit"] = firstA.hit
	rep.Details["first_run_b_hit"] = firstB.hit
	return rep
}
