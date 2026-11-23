package pipeline

import (
	"crypto/sha256"
	"encoding/hex"

	"mediaqc/internal/buffer"
	"mediaqc/internal/model"
	"mediaqc/internal/recovery"
)

// Material is one independently processed input unit.
type Material struct {
	ID     string
	Events []model.Event
	// Gaps declared by parse/buffer stages before recovery runs.
	Gaps []model.Gap
	// FEC groups protecting the material's RTP packets (may be empty).
	FECGroups []recovery.Group
}

// MaterialResult is the per-material deterministic outcome.
type MaterialResult struct {
	MaterialID        string        `json:"material_id"`
	Status            string        `json:"status"` // ok, degraded, failed
	Output            []model.Event `json:"-"`
	OutputSequences   []uint64      `json:"output_sequences"`
	Checkpoints       []Checkpoint  `json:"checkpoints"`
	Recovered         []uint64      `json:"recovered_sequences"`
	UnrecoveredGaps   []model.Gap   `json:"unrecovered_gaps"`
	Errors            []string      `json:"errors"`
	InvariantFailures []string      `json:"invariant_failures"`
}

// Config controls batch execution.
type Config struct {
	WindowSize  int
	ToleranceMs int64
}

// Batch runs the staged pipeline over many materials with error isolation.
type Batch struct {
	cfg          Config
	checkpointer *Checkpointer
	results      []MaterialResult
}

// NewBatch builds a batch runner.
func NewBatch(cfg Config) *Batch {
	return &Batch{cfg: cfg, checkpointer: NewCheckpointer()}
}

// Run processes every material independently: a failure in one never aborts
// the others.
func (b *Batch) Run(materials []Material) []MaterialResult {
	for _, mat := range materials {
		b.results = append(b.results, b.runOne(mat))
	}
	return b.results
}

func (b *Batch) runOne(mat Material) MaterialResult {
	res := MaterialResult{MaterialID: mat.ID, Status: "ok"}
	hadUnrecoverable := false
	recordErr := func(err error) {
		if err == nil {
			return
		}
		res.Errors = append(res.Errors, err.Error())
		if !model.IsRecoverable(err) {
			hadUnrecoverable = true
		}
	}

	sourceSeqs := map[uint64]bool{}
	for _, ev := range mat.Events {
		sourceSeqs[ev.Sequence] = true
	}

	// Stage: buffer (reorder/dedup/window).
	jb, err := buffer.New(mat.ID, "primary", b.cfg.WindowSize)
	if err != nil {
		recordErr(err)
		return b.finish(res, hadUnrecoverable)
	}
	var ordered []model.Event
	for _, ev := range mat.Events {
		out, perr := jb.Push(ev)
		if perr != nil {
			recordErr(perr)
			continue
		}
		ordered = append(ordered, out...)
	}
	flushed, bufStats := jb.Flush()
	ordered = append(ordered, flushed...)
	res.UnrecoveredGaps = append(res.UnrecoveredGaps, bufStats.UnrecoveredGaps...)
	b.saveCP(&res, mat.ID, model.StageBuffer, ordered, bufStats.UnrecoveredGaps, nil)

	// Stage: recovery (single-XOR FEC against declared gaps).
	dec := recovery.NewDecoder(mat.ID, "primary")
	for _, g := range mat.FECGroups {
		recordErr(dec.AddGroup(g))
	}
	for _, ev := range ordered {
		dec.Observe(ev)
	}
	recovered, gaps, recErrs := dec.Recover(mat.Gaps)
	for _, e := range recErrs {
		recordErr(e)
	}
	res.Recovered = append(res.Recovered, recoveredSeq(recovered)...)
	ordered = mergeRecovered(ordered, recovered)
	// Gaps still unrecovered after FEC propagate to package/integrity status.
	for _, g := range gaps {
		if !g.Recovered {
			res.UnrecoveredGaps = appendGap(res.UnrecoveredGaps, g)
		}
	}
	b.saveCP(&res, mat.ID, model.StageRecovery, ordered, gaps, res.Recovered)

	// Stage: sync + package checkpoints (timing validated by invariants).
	b.saveCP(&res, mat.ID, model.StageSync, ordered, gaps, res.Recovered)
	b.saveCP(&res, mat.ID, model.StagePackage, ordered, gaps, res.Recovered)
	res.Output = ordered
	res.OutputSequences = sequences(ordered)

	// Cross-stage invariants.
	iv := NewInvariants()
	finalOK := !hadUnrecoverable
	iv.Check(mat.ID, sourceSeqs, ordered, mat.Gaps, hadUnrecoverable, finalOK)
	res.InvariantFailures = iv.Failures

	if iv.Failed() {
		res.Status = "failed"
	} else if hadUnrecoverable || len(res.UnrecoveredGaps) > 0 || len(res.Errors) > 0 {
		res.Status = "degraded"
	}
	return res
}

func (b *Batch) saveCP(res *MaterialResult, materialID string, stage model.Stage,
	evs []model.Event, gaps []model.Gap, recovered []uint64) {
	cp := Checkpoint{
		MaterialID:   materialID,
		Stage:        stage,
		EventCount:   len(evs),
		Sequences:    sequences(evs),
		Gaps:         append([]model.Gap(nil), gaps...),
		RecoveredSeq: append([]uint64(nil), recovered...),
		Status:       res.Status,
		PayloadHash:  hashEvents(evs),
	}
	if err := b.checkpointer.Save(cp); err != nil {
		res.InvariantFailures = append(res.InvariantFailures, err.Error())
	}
	res.Checkpoints = append(res.Checkpoints, cp)
}

func (b *Batch) finish(res MaterialResult, hadUnrecoverable bool) MaterialResult {
	if hadUnrecoverable {
		res.Status = "failed"
	}
	return res
}

// CheckpointDigest exposes the deterministic checkpoint hash for rerun checks.
func (b *Batch) CheckpointDigest() string { return b.checkpointer.Digest() }

// Results returns the per-material results.
func (b *Batch) Results() []MaterialResult { return b.results }

func mergeRecovered(ordered []model.Event, recovered []model.Event) []model.Event {
	have := map[uint64]bool{}
	for _, ev := range ordered {
		have[ev.Sequence] = true
	}
	for _, ev := range recovered {
		if !have[ev.Sequence] {
			ordered = append(ordered, ev)
		}
	}
	sortEvents(ordered)
	return ordered
}

func hashEvents(evs []model.Event) string {
	h := sha256.New()
	for _, ev := range evs {
		buf := make([]byte, 8)
		putUint64(buf, ev.Sequence)
		h.Write(buf)
		h.Write(ev.Payload)
	}
	return hex.EncodeToString(h.Sum(nil))
}
