package pipeline

import (
	"fmt"
	"sort"

	"mediad/internal/clock"
	"mediad/internal/index"
	"mediad/internal/manifest"
	mp4 "mediad/internal/mp4"
)

// VerifyCheck is one verification assertion.
type VerifyCheck struct {
	Name      string `json:"name"`
	Camera    string `json:"camera,omitempty"`
	TrackID   uint32 `json:"track_id,omitempty"`
	RawOffset int64  `json:"raw_offset,omitempty"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail,omitempty"`
}

// VerifyResult is the outcome of the verification stage.
type VerifyResult struct {
	Checks []VerifyCheck `json:"checks"`
	OK     bool          `json:"ok"`
}

// Failure returns a structured verification error when any check failed, so a
// failed delivery can never be reported as success. The error carries the
// first failing camera's track and raw byte offset when known.
func (v *VerifyResult) Failure() error {
	if v == nil || v.OK {
		return nil
	}
	for _, c := range v.Checks {
		if c.OK {
			continue
		}
		e := clock.New(clock.StageVerify, clock.CodeVerification, "%s: %s", c.Name, c.Detail)
		if c.TrackID != 0 {
			e = e.WithTrack(c.TrackID)
		}
		if c.RawOffset > 0 {
			e = e.WithOffset(c.RawOffset)
		}
		if c.Camera != "" {
			e = e.WithCamera(c.Camera)
		}
		return e
	}
	return clock.New(clock.StageVerify, clock.CodeVerification, "verification failed")
}

// verify runs cross-stage checks: timeline monotonicity, index coverage,
// edit-list containment and manifest alignment.
func verify(repairs map[string]*mp4.Repaired, indexes map[string]*index.Index, man *manifest.Manifest, opts Options) *VerifyResult {
	v := &VerifyResult{OK: true}
	add := func(c VerifyCheck) {
		if !c.OK {
			v.OK = false
		}
		v.Checks = append(v.Checks, c)
	}

	cams := make([]string, 0, len(repairs))
	for c := range repairs {
		cams = append(cams, c)
	}
	sort.Strings(cams)

	for _, e := range man.Entries {
		rep := repairs[e.Camera]
		if rep == nil || len(rep.Tracks) == 0 {
			add(VerifyCheck{Name: "repair_present", Camera: e.Camera, OK: false, Detail: "no repaired track"})
			continue
		}
		tr := &rep.Tracks[0]
		add(VerifyCheck{Name: "repair_present", Camera: e.Camera, OK: true,
			Detail: fmt.Sprintf("%d samples, %d repairs", len(tr.Samples), tr.RepairCount)})

		// Timeline must be strictly increasing after repair.
		mono, at := monotonic(tr)
		add(VerifyCheck{Name: "timeline_monotonic", Camera: e.Camera, OK: mono,
			Detail: monoDetail(mono, at)})

		// Edit list must not claim more media than exists. An overrun means the
		// delivery interval is longer than the material, so verification fails.
		ok, detail := editContainment(tr)
		vc := VerifyCheck{Name: "edit_list_within_media", Camera: e.Camera, OK: ok, Detail: detail}
		if !ok {
			vc.TrackID = tr.TrackID
			if len(tr.EditList.Entries) > 0 {
				vc.RawOffset = tr.EditList.Entries[0].RawOffset
			}
		}
		add(vc)

		// Index coverage and monotonicity.
		idx := indexes[e.Camera]
		if idx == nil || len(idx.Tracks) == 0 {
			add(VerifyCheck{Name: "index_present", Camera: e.Camera, OK: false, Detail: "no index"})
			continue
		}
		ti := &idx.Tracks[0]
		if err := ti.Validate(); err != nil {
			add(VerifyCheck{Name: "index_valid", Camera: e.Camera, OK: false, Detail: err.Error()})
		} else {
			add(VerifyCheck{Name: "index_valid", Camera: e.Camera, OK: true})
		}
		add(VerifyCheck{Name: "index_coverage", Camera: e.Camera, OK: ti.CoveragePct >= 99.5,
			Detail: fmt.Sprintf("%.2f%%", ti.CoveragePct)})

		// A track that lacked sync flags must disclose that its index is
		// derived, so a reviewer knows seek points were inferred.
		if !tr.SyncFromFlags {
			add(VerifyCheck{Name: "sync_flags_disclosed", Camera: e.Camera,
				OK:     ti.DerivedSync,
				Detail: "source had no sync table; index must be marked derived"})
		} else {
			add(VerifyCheck{Name: "sync_flags_disclosed", Camera: e.Camera, OK: true, Detail: "from stss"})
		}

		// If the network sidecar crossed the 32-bit boundary, the extension
		// must have recovered monotonic time.
		if tr.RTP != nil {
			ok := tr.RTP.WrapCount == 0 || tr.RTP.ExtendedLast > uint64(tr.RTP.FirstTimestamp)
			add(VerifyCheck{Name: "rtp_wrap_extended", Camera: e.Camera, OK: ok,
				Detail: fmt.Sprintf("wraps=%d extended_last=%d", tr.RTP.WrapCount, tr.RTP.ExtendedLast)})
		}

		// Manifest alignment: the editorial in-point must land within one frame
		// of a delivered frame.
		align := alignment(tr, e)
		add(VerifyCheck{Name: "manifest_alignment", Camera: e.Camera, OK: align <= frameTolerance(tr),
			Detail: fmt.Sprintf("%.3fms", float64(align)/1e6)})
	}
	return v
}

func monotonic(tr *mp4.RepairedTrack) (bool, int) {
	for i := 1; i < len(tr.Samples); i++ {
		if tr.Samples[i].DTS <= tr.Samples[i-1].DTS || tr.Samples[i].PTS < tr.Samples[i].DTS {
			return false, i
		}
	}
	return true, -1
}

func monoDetail(ok bool, at int) string {
	if ok {
		return "strictly increasing DTS, PTS >= DTS"
	}
	return fmt.Sprintf("violation at sample index %d", at)
}

// editContainment compares the presented media span with the edit-list
// duration. The edit list may not extend past the last presented sample by
// more than one frame; anything more is an overrun.
func editContainment(tr *mp4.RepairedTrack) (bool, string) {
	if len(tr.EditList.Entries) == 0 || len(tr.Samples) == 0 {
		return true, "no edit list"
	}
	declared := tr.EditList.TotalNs()
	presented := tr.LastNs - tr.FirstNs
	if presented <= 0 {
		return false, "presented span is not positive"
	}
	slack := frameTolerance(tr)
	if declared > presented+slack {
		overrun := declared - presented
		return false, fmt.Sprintf("edit list declares %d ns but media presents %d ns (overrun %d ns)",
			declared, presented, overrun)
	}
	return true, fmt.Sprintf("edit list %d ns within media %d ns", declared, presented)
}

func frameTolerance(tr *mp4.RepairedTrack) int64 {
	if len(tr.Samples) < 2 {
		return int64(clock.NsPerSecond)
	}
	d := tr.Samples[1].NS - tr.Samples[0].NS
	if d < 0 {
		d = -d
	}
	if d == 0 {
		return int64(clock.NsPerSecond)
	}
	// One frame of slack plus one millisecond of rounding headroom.
	return d + int64(1_000_000)
}

// alignment returns the distance from the manifest editorial in-point to the
// nearest delivered frame.
func alignment(tr *mp4.RepairedTrack, e manifest.Entry) int64 {
	want := e.Editorial.StartNs - e.Recording.StartNs
	var best int64 = -1
	for _, s := range tr.Samples {
		d := s.NS - want
		if d < 0 {
			d = -d
		}
		if best < 0 || d < best {
			best = d
		}
		if s.NS > want+int64(clock.NsPerSecond) {
			break
		}
	}
	if best < 0 {
		return int64(^uint64(0) >> 1)
	}
	return best
}
