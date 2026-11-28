package demo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"mediapipe/internal/media"
	"mediapipe/internal/model"
	mp4 "mediapipe/internal/mp4"
)

// damageReport is the outcome of the damaged-fixture checks.
type damageReport struct {
	Cases []damageCase
	// failed is set when a fixture did not fail as required, or when a
	// failure leaked a partially confirmed result.
	failed bool
	err    error
}

// damageCase records what the scanner reported for one damaged fixture.
type damageCase struct {
	Name    string
	Damage  string
	OK      bool
	Asset   string
	Version string
	Stage   string
	Track   string
	Offset  int64
	Kind    string
	Detail  string
}

// runDamageFixtures builds deliberately broken renditions and requires the
// scanner to reject each one with asset, stage and byte offset, never
// returning a partially confirmed index.
func runDamageFixtures(ctx context.Context, a assetPlan, o Options) *damageReport {
	rep := &damageReport{}
	vp := a.versions[0]
	for _, tp := range vp.tracks {
		spec, err := fileSpec(a, vp, tp)
		if err != nil {
			rep.fail("plan", err)
			continue
		}
		base, err := media.BuildFile(spec)
		if err != nil {
			rep.fail("build", err)
			continue
		}

		// 1. Truncation: cut the encoded file in the middle of its media.
		cut := len(base) * 2 / 3
		rep.check(ctx, "truncated-box", media.DamageTruncate, a, vp, tp, base[:cut])

		// 2. Missing sample-size table: unrecoverable, must be reported.
		noSizes := append([]byte(nil), base...)
		if n := media.StripTable(noSizes, "stsz"); n == 0 {
			rep.fail("missing-sample-table", errors.New("fixture has no stsz table"))
		} else {
			rep.check(ctx, "missing-sample-table", media.DamageNoSizes, a, vp, tp, noSizes)
		}

		// 3. Damaged chunk offsets: recoverable, and the recovery must produce
		// a consistent index whose re-scan digest matches the clean file.
		badIdx := append([]byte(nil), base...)
		if n := media.StripTable(badIdx, "stco"); n == 0 {
			rep.fail("bad-index", errors.New("fixture has no stco table"))
		} else {
			rep.checkRecovery(ctx, a, vp, tp, base, badIdx)
		}
	}
	return rep
}

// check requires a damaged payload to fail with a structured error.
func (d *damageReport) check(ctx context.Context, name string, kind media.Damage, a assetPlan, vp versionPlan, tp trackPlan, data []byte) {
	err := scanForDamage(ctx, a.name, vp.id, tp.trackID, data)
	if err == nil {
		d.fail(name, fmt.Errorf("%s fixture was accepted", kind))
		return
	}
	me := model.AsError(err)
	if me.Kind != model.KindCorrupt {
		d.fail(name, fmt.Errorf("expected corrupt kind, got %s", me.Kind))
		return
	}
	if !me.HasOff || me.Asset == "" || me.Stage == "" {
		d.fail(name, fmt.Errorf("error lacks asset/stage/offset: %v", me))
		return
	}
	d.Cases = append(d.Cases, damageCase{
		Name: name, Damage: kind.String(), OK: true,
		Asset: me.Asset, Version: me.Version, Stage: string(me.Stage), Track: me.Track,
		Offset: me.Offset, Kind: me.Kind.String(),
		Detail: causeText(me),
	})
}

// checkRecovery requires a recoverable fixture to scan cleanly and to yield a
// summary identical to the undamaged file.
func (d *damageReport) checkRecovery(ctx context.Context, a assetPlan, vp versionPlan, tp trackPlan, clean, damaged []byte) {
	ci, err := scanData(ctx, a.name, vp.id, tp.trackID, clean)
	if err != nil {
		d.fail("recovery-clean", err)
		return
	}
	di, err := scanData(ctx, a.name, vp.id, tp.trackID, damaged)
	if err != nil {
		d.fail("bad-index", err)
		return
	}
	if !di.Recovered {
		d.fail("bad-index", errors.New("damaged index was not marked recovered"))
		return
	}
	if indexDigest(ci) != indexDigest(di) {
		d.fail("bad-index", errors.New("recovered index summary differs from the clean index"))
		return
	}
	d.Cases = append(d.Cases, damageCase{
		Name: "bad-index", Damage: media.DamageBadIndex.String(), OK: true,
		Asset: a.name, Version: vp.id, Stage: "mp4_index", Track: tp.trackID,
		Kind: "recovered", Detail: "digest matches clean scan",
	})
}

// indexDigest hashes the locatable sample layout of an index.
func indexDigest(idx *mp4.Index) string {
	var b strings.Builder
	for _, t := range idx.Tracks {
		fmt.Fprintf(&b, "t%d/%s/%d;", t.TrackID, t.Handler, t.Timescale)
		for _, s := range t.Samples {
			fmt.Fprintf(&b, "%d:%d:%d:%d:%v;", s.Index, s.Offset, s.Size, s.DecodeTks, s.Sync)
		}
	}
	return b.String()
}

func (d *damageReport) fail(name string, err error) {
	if !d.failed {
		d.failed = true
		d.err = model.New(model.KindCorrupt, model.StageMP4Scan, "damage-fixture", "check", err).
			WithVersion(name)
	}
	d.Cases = append(d.Cases, damageCase{Name: name, OK: false, Detail: err.Error()})
}

func causeText(me *model.Error) string {
	if me.Cause == nil {
		return ""
	}
	return me.Cause.Error()
}

// scanForDamage scans bytes and returns any rejection. Recovery is permitted
// for incomplete indexes, but a truncated container or an unparseable table
// must fail with a structured error.
func scanForDamage(ctx context.Context, asset, version, track string, data []byte) error {
	s, err := mp4.NewStream(readerAt(data), int64(len(data)), asset, mp4.DefaultConfig())
	if err != nil {
		return annotate(err, asset, version, track)
	}
	idx, err := mp4.BuildIndex(ctx, s)
	if err != nil {
		return annotate(err, asset, version, track)
	}
	if err := idx.Validate(int64(len(data)), asset); err != nil {
		return annotate(err, asset, version, track)
	}
	if idx.Recovered {
		return model.New(model.KindCorrupt, model.StageIndex, asset, "damage-check",
			errors.New("index required recovery; this fixture must be rejected")).
			WithVersion(version).WithTrack(track)
	}
	return nil
}

// render produces the fixed-order terminal report.
func render(out *Outcome) []string {
	m := out.Metrics
	var b []string
	add := func(format string, args ...any) { b = append(b, fmt.Sprintf(format, args...)) }
	add("mediapipe regression demo")
	add("seed=%d versions=%d window=%d", out.Summary.Seed, out.Summary.Versions, out.Summary.Window)
	add("")
	add("av_sync_error_ms=%d", m.AVSyncErrorMS())
	add("loss_recovery_rate=%.4f", m.LossRecoveryRate())
	add("latency_p95_ms=%.3f", m.LatencyP95MS())
	add("missing=%d", m.Missing)
	add("conflicts=%d", m.Conflicts)
	add("unresolved_conflicts=%d", m.UnresolvedConflicts)
	add("recovered_indexes=%d", m.RecoveredIndexes)
	add("packets=%d duplicates=%d", m.Packets, m.Duplicates)
	add("rollback_status=%s", out.Summary.Status.State)
	if out.Summary.Status.State == "committed" {
		add("manifest_digest=%s", out.Digest)
	} else {
		add("rollback_reason=%s", out.Summary.Status.Reason)
		for _, r := range out.Summary.Status.Records {
			add("  rollback asset=%s version=%s track=%s stage=%s kind=%s offset=%d reason=%s",
				r.Asset, r.Version, r.Track, r.Stage, r.Kind, r.Offset, r.Reason)
		}
	}
	for _, am := range out.Merges {
		add("merge[%s] tracks=%d missing=%d conflicts=%d resolved=%d",
			am.Asset, len(am.Merged.Tracks), am.Merged.MissingCount(),
			am.Merged.ConflictCount(), am.Merged.ConflictCount()-am.Merged.UnresolvedCount())
	}
	add("")
	add("sparse_keyframes: samples=%d probes=%d keyframe_seeks=%d max_deviation_ms=%d mean_deviation_ms=%d",
		out.Seek.Samples, out.Seek.Probes, out.Seek.KeySeeked, out.Seek.MaxDevMS, out.Seek.MeanDevMS)
	add("failure scenario: status=%s rerun_digest_match=%v records=%d",
		out.Failure.Status, out.Failure.Digest == out.Failure.RerunDigest, len(out.Failure.Records))
	for _, r := range out.Failure.Records {
		add("  failure asset=%s version=%s track=%s stage=%s kind=%s offset=%d",
			r.Asset, r.Version, r.Track, r.Stage, r.Kind, r.Offset)
	}
	add("recovery scenario: status=%s recovered_tracks=%d rescan_digest_match=%v",
		out.Recovery.Status, out.Recovery.Recovered, out.Recovery.Digest == out.Recovery.RescanDigest)
	if len(out.Damage) > 0 {
		add("")
		add("damaged fixtures (%d):", len(out.Damage))
		for _, c := range out.Damage {
			if c.OK {
				add("  %-22s asset=%s version=%s track=%s stage=%s offset=%d kind=%s",
					c.Name, c.Asset, c.Version, c.Track, c.Stage, c.Offset, c.Kind)
			} else {
				add("  %-22s FAILED: %s", c.Name, c.Detail)
			}
		}
	}
	return b
}

// SortedDamage returns damage cases ordered by name.
func (o *Outcome) SortedDamage() []damageCase {
	out := append([]damageCase(nil), o.Damage...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// String renders an outcome as one newline-joined block.
func (o *Outcome) String() string { return strings.Join(o.Lines, "\n") }
