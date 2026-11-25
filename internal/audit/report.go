// Package audit reconciles source events against remuxed output events
// and reports item counts, time deviation and payload byte deviation.
// The lossless path requires byte deviation 0 and time deviation within
// the configured tolerance.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"mediacore/internal/clock"
	"mediacore/internal/model"
)

// Report summarizes one sample's pipeline outcome for terminal review.
type Report struct {
	SampleID        string              `json:"sample_id"`
	TaskKey         string              `json:"task_key"`
	Events          int                 `json:"events"`
	Items           int                 `json:"items"`
	MaxTimeDevTicks int64               `json:"max_time_dev_ticks"`
	ToleranceTicks  int64               `json:"tolerance_ticks"`
	ByteDeviation   int64               `json:"byte_deviation"`
	PayloadSHA256   string              `json:"payload_sha256"`
	Drift           []clock.DriftReport `json:"drift,omitempty"`
	OK              bool                `json:"ok"`
}

func shaOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PayloadDigest is a stable digest over all sample payloads in
// (ticks, track, offset) order.
func PayloadDigest(events []model.Event) string {
	type ref struct {
		ticks int64
		track string
		off   int64
		sha   string
	}
	refs := make([]ref, 0, len(events))
	for _, e := range events {
		refs = append(refs, ref{e.Ticks, e.TrackID, e.SrcOffset, shaOf(e.Payload)})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ticks != refs[j].ticks {
			return refs[i].ticks < refs[j].ticks
		}
		if refs[i].track != refs[j].track {
			return refs[i].track < refs[j].track
		}
		return refs[i].off < refs[j].off
	})
	h := sha256.New()
	for _, r := range refs {
		h.Write([]byte(r.sha))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Compare reconciles source events with events re-demuxed from the
// remuxed output. Events are matched by payload digest; time deviation
// is the largest tick difference among matched pairs, byte deviation
// the total payload bytes that could not be matched (both directions).
func Compare(sampleID, taskKey string, src, out []model.Event, tolerance int64, drift []clock.DriftReport) Report {
	rep := Report{
		SampleID: sampleID, TaskKey: taskKey,
		Events: len(src), Items: len(out),
		ToleranceTicks: tolerance,
		PayloadSHA256:  PayloadDigest(src),
		Drift:          drift,
	}
	type key struct {
		kind model.EventKind
		sha  string
	}
	type outRef struct {
		ticks int64
		size  uint32
	}
	unmatched := map[key][]outRef{}
	for _, e := range out {
		k := key{e.Kind, shaOf(e.Payload)}
		unmatched[k] = append(unmatched[k], outRef{e.Ticks, e.Size})
	}
	for _, e := range src {
		k := key{e.Kind, shaOf(e.Payload)}
		list := unmatched[k]
		if len(list) == 0 {
			rep.ByteDeviation += int64(e.Size)
			continue
		}
		// Nearest-tick match for this payload.
		best, bestDev := 0, int64(0)
		for i, r := range list {
			d := r.ticks - e.Ticks
			if d < 0 {
				d = -d
			}
			if i == 0 || d < bestDev {
				best, bestDev = i, d
			}
		}
		if bestDev > rep.MaxTimeDevTicks {
			rep.MaxTimeDevTicks = bestDev
		}
		unmatched[k] = append(list[:best], list[best+1:]...)
	}
	for _, list := range unmatched {
		for _, r := range list {
			// Leftover output payload bytes that match nothing in source.
			rep.ByteDeviation += int64(r.size)
		}
	}
	rep.OK = rep.ByteDeviation == 0 && rep.MaxTimeDevTicks <= tolerance
	return rep
}
