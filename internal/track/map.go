// Package track enforces explicit one-to-one mapping between recovered
// source tracks and declared output tracks. A mapping is only committed when
// every requirement is satisfied; every rejection reason is retained.
package track

import (
	"strconv"

	"mediarc/internal/model"
)

// OutputSpec declares one required output track and the source role that must
// feed it. Roles are codec-level identifiers (e.g. "video", "audio").
type OutputSpec struct {
	OutputID   uint32
	Role       string
	Timescale  uint32
}

// Source is one recoverable input track with the representation it belongs to
// and the role it can serve.
type Source struct {
	RepID     string
	Track     model.RecoveredTrack
	Role      string
}

// Rejection records one reason an output mapping could not be committed.
type Rejection struct {
	OutputID uint32
	Kind     model.ErrorKind
	Detail   string
}

// MappingResult is the verdict for one full mapping attempt.
type MappingResult struct {
	// Committed pairs output id to the chosen source. Empty when Accepted is
	// false: a partial mapping is never returned as success.
	Committed map[uint32]Source
	Accepted  bool
	Rejections []Rejection
}

// Plan is the complete explicit mapping request: every required output and
// every source allowed to satisfy it.
type Plan struct {
	Outputs []OutputSpec
	Sources []Source
}

// Submit validates and commits the plan. Rules, evaluated independently so
// every applicable rejection is reported:
//   - each output must have exactly one eligible source (missing track);
//   - one source may never serve two outputs (duplicate mapping);
//   - unmappable/unknown codecs cannot be assigned to an output;
//   - source and output timescales must be compatible (equal and non-zero).
func (p Plan) Submit() MappingResult {
	res := MappingResult{Committed: map[uint32]Source{}}
	usage := map[string]int{} // "repID:trackID" -> output count
	chosen := map[uint32]Source{}

	for _, out := range p.Outputs {
		var eligible []Source
		for _, src := range p.Sources {
			if src.Role != out.Role {
				continue
			}
			if src.Track.Track.NotMappable {
				res.Rejections = append(res.Rejections, Rejection{
					OutputID: out.OutputID, Kind: model.KindTrackMissing,
					Detail: "role " + out.Role + " source " + src.RepID + "/" +
						key(src.Track.Track.ID) + " carries an unmappable codec",
				})
				continue
			}
			eligible = append(eligible, src)
		}
		if len(eligible) == 0 {
			res.Rejections = append(res.Rejections, Rejection{
				OutputID: out.OutputID, Kind: model.KindTrackMissing,
				Detail: "no source track serves role " + out.Role,
			})
			continue
		}
		if len(eligible) > 1 {
			res.Rejections = append(res.Rejections, Rejection{
				OutputID: out.OutputID, Kind: model.KindTrackDuplicate,
				Detail: "role " + out.Role + " has " + strconv.Itoa(len(eligible)) +
					" candidate sources; explicit mapping requires exactly one",
			})
			continue
		}
		src := eligible[0]
		if !timescaleCompatible(src.Track.Track.Timescale, out.Timescale) {
			res.Rejections = append(res.Rejections, Rejection{
				OutputID: out.OutputID, Kind: model.KindTimescaleIncompat,
				Detail: "source timescale " + strconv.FormatUint(uint64(src.Track.Track.Timescale), 10) +
					" incompatible with output timescale " + strconv.FormatUint(uint64(out.Timescale), 10),
			})
			continue
		}
		chosen[out.OutputID] = src
		usage[src.RepID+":"+key(src.Track.Track.ID)]++
	}

	// Duplicate-mapping pass: the same source committed to several outputs.
	for k, n := range usage {
		if n > 1 {
			for _, out := range p.Outputs {
				if s, ok := chosen[out.OutputID]; ok && s.RepID+":"+key(s.Track.Track.ID) == k {
					res.Rejections = append(res.Rejections, Rejection{
						OutputID: out.OutputID, Kind: model.KindTrackDuplicate,
						Detail: "source " + k + " mapped to " + strconv.Itoa(n) + " outputs",
					})
				}
			}
		}
	}

	if len(res.Rejections) == 0 && len(chosen) == len(p.Outputs) {
		res.Accepted = true
		res.Committed = chosen
	}
	return res
}

func timescaleCompatible(src, out uint32) bool {
	return src != 0 && out != 0 && src == out
}

func key(id uint32) string { return strconv.FormatUint(uint64(id), 10) }
