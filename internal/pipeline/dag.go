package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"archivecheck/internal/checkpoint"
	"archivecheck/internal/fixture"
	"archivecheck/internal/inspect"
	"archivecheck/internal/manifest"
	"archivecheck/internal/model"
	"archivecheck/internal/scheduler"
)

// outcomeWire is the auditable payload recorded in a node's artifact detail so
// a resumed run can replay the full result without touching the media again.
type outcomeWire struct {
	Result   SegmentResult `json:"result,omitempty"`
	Damage   []damageWire  `json:"damage,omitempty"`
	Fault    *faultWire    `json:"fault,omitempty"`
	Merged   bool          `json:"merged,omitempty"`
	Verified bool          `json:"verified,omitempty"`
	// Gaps carries a reorder node's transport gaps into a resumed run.
	Gaps []transportGap `json:"gaps,omitempty"`
}

type damageWire struct {
	Start           int64    `json:"start"`
	End             int64    `json:"end"`
	Confidence      string   `json:"confidence"`
	Representations []string `json:"representations"`
	Segments        []string `json:"segments"`
	MissingPackets  uint64   `json:"missing_packets"`
	Causes          []string `json:"causes"`
	Timebase        int64    `json:"timebase"`
}

type faultWire struct {
	Code    model.Code     `json:"code"`
	Message string         `json:"message"`
	Source  model.Source   `json:"source"`
	Loc     model.Location `json:"location"`
}

// dagState is shared by the node closures.
type dagState struct {
	mu       sync.Mutex
	reorder  map[string]*reorderOutcome
	inspect  map[string]*taskOutcome
	entries  []checkpoint.Entry
	cached   int
	damage   []damageWire
	firstBad string
}

// reorderOutcome is what the reorder node learned about one segment.
type reorderOutcome struct {
	result SegmentResult
	fault  *model.Fault
	// gaps are the transport gaps, already sorted, pending media mapping by
	// the inspect node.
	gaps []transportGap
}

// transportGap mirrors transport.Gap so the pipeline stays serialisable.
type transportGap struct {
	Before         uint64
	After          uint64
	Count          uint64
	FirstTimestamp uint32
	LastTimestamp  uint32
}

// runDAG builds and executes the analysis graph:
//
//	reorder/<rep>/<seg> -> inspect/<rep>/<seg>  (per segment)
//	                  \-> merge                  (all inspections)
//	                       \-> certify            (only on a clean merge)
func runDAG(ctx context.Context, cfg Config, fx *fixture.Manifest, plan *manifest.Plan, tasks []segmentTask, entries []checkpoint.Entry) (*dagResult, error) {
	st := &dagState{reorder: map[string]*reorderOutcome{}, inspect: map[string]*taskOutcome{}, entries: entries}
	var nodes []scheduler.Node
	var inspectIDs []string
	for _, t := range tasks {
		t := t
		reorderID := "reorder/" + t.nodeID()
		inspectID := "inspect/" + t.nodeID()
		nodes = append(nodes, scheduler.Node{
			ID:           reorderID,
			InputDigest:  t.inputDigest("reorder"),
			ParamsDigest: t.paramsDigest("reorder"),
			MaxAttempts:  2,
			Run: func(_ context.Context, key scheduler.Key) (scheduler.Artifact, error) {
				oc := t.runReorder(cfg)
				st.mu.Lock()
				st.reorder[t.nodeID()] = oc
				st.mu.Unlock()
				art, _ := json.Marshal(outcomeWire{Result: oc.result, Fault: wireFault(oc.fault), Gaps: oc.gaps})
				return scheduler.Artifact{Digest: oc.result.OutputSHA256, Size: 0, Detail: string(art)}, nil
			},
		})
		nodes = append(nodes, scheduler.Node{
			ID:           inspectID,
			Deps:         []string{reorderID},
			InputDigest:  t.inputDigest("inspect"),
			ParamsDigest: t.paramsDigest("inspect"),
			MaxAttempts:  1,
			Run: func(_ context.Context, key scheduler.Key) (scheduler.Artifact, error) {
				st.mu.Lock()
				prev := st.reorder[t.nodeID()]
				st.mu.Unlock()
				oc := t.runInspect(cfg, prev)
				st.mu.Lock()
				st.inspect[t.nodeID()] = oc
				st.mu.Unlock()
				art, _ := json.Marshal(outcomeWire{
					Result: oc.result,
					Damage: damageToWire(oc.damage),
					Fault:  wireFaultFor(oc.blockingFault(), t.representation, t.seg.ID),
				})
				a := scheduler.Artifact{Digest: oc.result.OutputSHA256, Size: oc.result.Duration, Detail: string(art)}
				// A segment that cannot be certified intact blocks the merge
				// path. Structural damage and located media damage both do so;
				// the difference is that located damage is still reported as a
				// damage range with media-time bounds.
				if oc.blockingFault() != nil {
					return a, oc.blockingFault()
				}
				return a, nil
			},
		})
		inspectIDs = append(inspectIDs, inspectID)
	}
	nodes = append(nodes, scheduler.Node{
		ID:           "merge",
		Deps:         inspectIDs,
		InputDigest:  planDigest(plan, "merge"),
		ParamsDigest: configDigest(cfg, "merge"),
		MaxAttempts:  1,
		Run: func(_ context.Context, _ scheduler.Key) (scheduler.Artifact, error) {
			wires, _ := st.collectDamage()
			art, _ := json.Marshal(outcomeWire{Damage: wires, Merged: true})
			return scheduler.Artifact{Digest: digestString(fmt.Sprintf("%v", wires)), Detail: string(art)}, nil
		},
	})
	nodes = append(nodes, scheduler.Node{
		ID:           "certify",
		Deps:         []string{"merge"},
		InputDigest:  planDigest(plan, "certify"),
		ParamsDigest: configDigest(cfg, "certify"),
		MaxAttempts:  1,
		Run: func(_ context.Context, _ scheduler.Key) (scheduler.Artifact, error) {
			// Certification only runs when every inspection succeeded, which
			// means every segment decoded completely and no damage was found.
			return scheduler.Artifact{Digest: digestString("verified"), Detail: `{"verified":true}`}, nil
		},
	})

	g, err := scheduler.NewGraph(nodes)
	if err != nil {
		return nil, err
	}
	byNode := map[string]segmentTask{}
	for _, t := range tasks {
		byNode["reorder/"+t.nodeID()] = t
		byNode["inspect/"+t.nodeID()] = t
	}
	r := &scheduler.Runner{
		Concurrency: cfg.Concurrency,
		OnTerminal: func(res scheduler.NodeResult) error {
			st.mu.Lock()
			defer st.mu.Unlock()
			if t, ok := byNode[res.ID]; ok {
				phase := "reorder"
				if res.ID[:len("inspect/")] == "inspect/" {
					phase = "inspect"
				}
				key := scheduler.Key{NodeID: res.ID, InputDigest: t.inputDigest(phase), ParamsDigest: t.paramsDigest(phase)}
				st.entries = checkpoint.Add(st.entries, key, res, cfg.Now())
			} else {
				// Synthetic nodes depend only on the plan and configuration.
				key := scheduler.Key{
					NodeID:       res.ID,
					InputDigest:  planDigest(plan, res.ID),
					ParamsDigest: configDigest(cfg, res.ID),
				}
				st.entries = checkpoint.Add(st.entries, key, res, cfg.Now())
			}
			if res.Cached {
				st.cached++
				// Only a replayed node needs its outcome rebuilt from the
				// recorded artifact; a freshly executed node already recorded
				// its live outcome in the shared state.
				st.rehydrate(res)
			}
			return nil
		},
	}
	if cfg.Resume {
		r.Completed = checkpoint.Completed(entries)
	}
	report, err := r.Run(ctx, g)
	if err != nil {
		return nil, err
	}
	return &dagResult{report: report, state: st}, nil
}

// collectDamage returns the merged damage in a deterministic order.
func (st *dagState) collectDamage() ([]damageWire, string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	var merged []damageWire
	for _, oc := range st.inspect {
		merged = append(merged, damageToWire(oc.damage)...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Start != merged[j].Start {
			return merged[i].Start < merged[j].Start
		}
		return merged[i].End < merged[j].End
	})
	st.damage = merged
	st.firstBad = ""
	for _, d := range merged {
		if len(d.Segments) > 0 {
			st.firstBad = d.Segments[0]
			break
		}
	}
	return merged, st.firstBad
}

// rehydrate restores outcomes cached from a previous run.
func (st *dagState) rehydrate(res scheduler.NodeResult) {
	if res.Status == scheduler.StatusBlocked {
		// A replayed block has no analysis payload; the merge node will still
		// be blocked because the inspect node's dependency is terminal.
		return
	}
	var w outcomeWire
	if err := json.Unmarshal([]byte(res.Artifact.Detail), &w); err != nil {
		return
	}
	f := unwireFault(w.Fault)
	oc := &taskOutcome{result: w.Result, damage: wireToDamage(w.Damage), fault: f}
	if f != nil && !isRecoverableNodeFault(f.Code) {
		oc.structural = f
	}
	switch {
	case len(res.ID) > 8 && res.ID[:8] == "reorder/":
		st.reorder[res.ID[len("reorder/"):]] = &reorderOutcome{result: oc.result, fault: oc.fault, gaps: w.Gaps}
	case len(res.ID) > 8 && res.ID[:8] == "inspect/":
		st.inspect[res.ID[len("inspect/"):]] = oc
	}
}

// digestString returns a short stable digest.
func digestString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// planDigest folds the plan identity into a node input digest.
func planDigest(plan *manifest.Plan, phase string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\x00", phase, plan.Source, plan.Timebase, plan.SegmentCount())
	for _, r := range plan.Representations {
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", r.ID, r.Timescale, len(r.Segments))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// configDigest folds tunables into a node parameter digest.
func configDigest(cfg Config, phase string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%d\x00%v\x00%d", phase, cfg.Timebase, cfg.ReorderWindow, cfg.AllowInexact, cfg.FrameTicks)
	return hex.EncodeToString(h.Sum(nil))
}

func wireFault(f *model.Fault) *faultWire {
	if f == nil {
		return nil
	}
	return &faultWire{Code: f.Code, Message: f.Message, Source: f.Source, Loc: f.Location}
}

// wireFaultFor is wireFault with the representation and segment pinned, so a
// replayed fault points at the media segment rather than the synthetic node ID.
func wireFaultFor(f *model.Fault, rep, seg string) *faultWire {
	w := wireFault(f)
	if w == nil {
		return nil
	}
	w.Source.Representation = rep
	w.Source.Segment = seg
	return w
}

func unwireFault(w *faultWire) *model.Fault {
	if w == nil {
		return nil
	}
	f := model.NewFault(w.Code, model.SeverityError, w.Source, "%s", w.Message)
	f.Location = w.Loc
	return f
}

func damageToWire(in []inspect.DamageRange) []damageWire {
	out := make([]damageWire, 0, len(in))
	for _, d := range in {
		causes := make([]string, 0, len(d.Causes))
		for _, c := range d.Causes {
			causes = append(causes, string(c))
		}
		out = append(out, damageWire{
			Start: d.Start, End: d.End, Confidence: string(d.Confidence),
			Representations: d.Representations, Segments: d.Segments,
			MissingPackets: d.MissingPackets, Causes: causes, Timebase: d.Timebase,
		})
	}
	return out
}

func wireToDamage(in []damageWire) []inspect.DamageRange {
	out := make([]inspect.DamageRange, 0, len(in))
	for _, d := range in {
		causes := make([]model.Code, 0, len(d.Causes))
		for _, c := range d.Causes {
			causes = append(causes, model.Code(c))
		}
		out = append(out, inspect.DamageRange{
			Start: d.Start, End: d.End, Confidence: inspect.Confidence(d.Confidence),
			Representations: d.Representations, Segments: d.Segments,
			MissingPackets: d.MissingPackets, Causes: causes, Timebase: d.Timebase,
		})
	}
	return out
}

// taskOutcome is one segment's analysis result.
type taskOutcome struct {
	result     SegmentResult
	damage     []inspect.DamageRange
	fault      *model.Fault
	structural *model.Fault
}

// isStructural reports whether a fault halts recovery of the segment. Located
// media damage and transport irregularities are recoverable; a malformed box
// or sample table is not, because nothing downstream can trust it.
func isStructural(code model.Code) bool {
	switch code {
	case model.CodeDamageGap, model.CodeDamageRepresentation, model.CodeTransportGap,
		model.CodeTransportDuplicate, model.CodeTransportShortRead, model.CodeTransportTimestamp:
		return false
	}
	return true
}

func isRecoverableNodeFault(code model.Code) bool { return !isStructural(code) }
