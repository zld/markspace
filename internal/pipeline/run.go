package pipeline

import (
	"time"

	"mediasync/internal/clock"
	"mediasync/internal/dag"
	"mediasync/internal/fixture"
	"mediasync/internal/store"
)

// Result bundles the summary with the underlying stage outputs so tests can
// assert invariants beyond the terminal JSON.
type Result struct {
	Summary Summary
	Journal *store.Journal
	Graph   *dag.Graph
	Recover *recoverOutput
	Index   *indexOutput
	Repair  *repairOutput
	Parse   *parseOutput
}

// Run executes one full deterministic processing pipeline.
func Run(opt Options) (*Result, error) {
	if err := validateOptions(opt); err != nil {
		return nil, err
	}
	cfg := fixture.DefaultAudioConfig(opt.Duration)
	plan := fixture.Plan{
		Seed: opt.Seed, LossBurst: opt.LossBurst, Reorder: opt.Reorder,
		TruncateAudio: opt.TruncateAudio, CorruptIndex: opt.CorruptIndex,
		ForceDependency: opt.ForceFail, FailNode: opt.FailNode,
	}
	stream, err := fixture.Generate(cfg, plan)
	if err != nil {
		return nil, err
	}

	// Fingerprint the pristine input: checkpoint reuse keys derive from it.
	inputDigest := MustDigest(map[string]any{
		"seed": opt.Seed, "duration_ms": opt.Duration.Milliseconds(),
		"loss": opt.LossBurst, "reorder": opt.Reorder,
		"truncate": opt.TruncateAudio, "corrupt": opt.CorruptIndex,
		"pcm": MustDigest(stream.PCM),
	})

	journal, err := openJournal(opt.JournalPath, inputDigest)
	if err != nil {
		return nil, err
	}
	var checkpoint dag.CheckpointStore
	if journal != nil {
		checkpoint = journal
	}

	nodes := buildNodes(stream, opt)
	graph, err := dag.NewGraph(nodes)
	if err != nil {
		return nil, err
	}
	sched, err := dag.NewScheduler(graph, opt.RetryLimit, checkpoint)
	if err != nil {
		return nil, err
	}
	schedResult := sched.Run()

	return assemble(stream, opt, schedResult, journal, graph)
}

// validateOptions enforces the input limits and parameter ranges.
func validateOptions(opt Options) error {
	if opt.Duration <= 0 {
		return clock.NewError(clock.ErrInvalidArgument, "duration must be positive")
	}
	if opt.LossBurst < 0 || opt.Reorder < 0 || opt.TruncateAudio < 0 ||
		opt.CorruptIndex < 0 || opt.RetryLimit < 0 {
		return clock.NewError(clock.ErrInvalidArgument, "negative fault/limit parameter")
	}
	cfg := fixture.DefaultAudioConfig(opt.Duration)
	n := cfg.PacketCount()
	if opt.LossBurst >= n {
		return clock.NewError(clock.ErrInvalidArgument,
			"loss burst %d leaves no packets (have %d)", opt.LossBurst, n)
	}
	if opt.TruncateAudio > 0 && int64(opt.TruncateAudio) >= mediaInputLimit(cfg) {
		return clock.NewError(clock.ErrInvalidArgument, "truncation removes the entire input")
	}
	return nil
}

func mediaInputLimit(cfg fixture.AudioConfig) int64 {
	return int64(cfg.PacketCount()*cfg.PacketSamples*cfg.BlockAlign()) + 44
}

func openJournal(path, digest string) (*store.Journal, error) {
	if path == "" {
		return nil, nil
	}
	return store.NewJournal(path, digest)
}

// buildNodes constructs the four-stage DAG with shared parameters folded
// into each idempotent key.
func buildNodes(stream *fixture.Stream, opt Options) []*dag.Node {
	common := map[string]string{
		"seed": itoa(opt.Seed), "duration_ms": itoa(opt.Duration.Milliseconds()),
		"loss": itoa(int64(opt.LossBurst)), "reorder": itoa(int64(opt.Reorder)),
		"truncate": itoa(int64(opt.TruncateAudio)), "corrupt": itoa(int64(opt.CorruptIndex)),
	}
	mk := func(name string, extra map[string]string, deps []string,
		factory func() any,
		fn func(*dag.Node, map[string]*dag.Node) (any, error)) *dag.Node {
		params := mergeParams(common, extra)
		return &dag.Node{
			Name: name, Deps: deps, Params: params,
			OutputFactory: factory, Run: maybeFail(name, opt, fn),
		}
	}
	parse := mk("parse", map[string]string{"stage": "parse"}, nil,
		func() any { return &parseOutput{} }, runParse(stream))
	repair := mk("repair", map[string]string{"stage": "repair"}, []string{"parse"},
		func() any { return &repairOutput{} }, runRepair(stream))
	recoverNode := mk("recover", map[string]string{"stage": "recover"}, []string{"repair"},
		func() any { return &recoverOutput{} }, runRecover())
	indexNode := mk("index", map[string]string{"stage": "index",
		"corrupt_count": itoa(int64(opt.CorruptIndex))}, []string{"parse", "recover"},
		func() any { return &indexOutput{} }, runIndex(opt.CorruptIndex))
	return []*dag.Node{parse, repair, recoverNode, indexNode}
}

// maybeFail wraps a node so the dependency-failure fixture can force the
// first ForceFail attempts of the named node to error.
func maybeFail(name string, opt Options,
	fn func(*dag.Node, map[string]*dag.Node) (any, error)) func(*dag.Node, map[string]*dag.Node) (any, error) {
	if opt.FailNode != name || opt.ForceFail <= 0 {
		return fn
	}
	attempt := 0
	return func(n *dag.Node, deps map[string]*dag.Node) (any, error) {
		attempt++
		if attempt <= opt.ForceFail {
			return nil, clock.NewError(clock.ErrDependency, "injected failure on node %q attempt %d", name, attempt)
		}
		return fn(n, deps)
	}
}

// assemble reads the scheduler outcome and stage outputs into a Summary.
func assemble(stream *fixture.Stream, opt Options, sr *dag.Result,
	journal *store.Journal, graph *dag.Graph) (*Result, error) {
	res := &Result{Journal: journal, Graph: graph}
	sum := Summary{
		Seed: opt.Seed, DurationMs: opt.Duration.Milliseconds(),
	}

	if p, ok := sr.Completed["parse"]; ok {
		res.Parse, _ = p.Output.(*parseOutput)
		if res.Parse != nil && res.Parse.Truncated {
			sum.TruncatedBytes = res.Parse.TruncMissing
		}
	}
	if r, ok := sr.Completed["repair"]; ok {
		res.Repair, _ = r.Output.(*repairOutput)
		if res.Repair != nil {
			sum.SyncErrorMs = res.Repair.SyncError
		}
	}
	if r, ok := sr.Completed["recover"]; ok {
		res.Recover, _ = r.Output.(*recoverOutput)
	} else if n, ok := graph.Node("recover"); ok && n.Output != nil {
		// Blocked recovery keeps its partial intervals/gaps for metrics.
		res.Recover, _ = n.Output.(*recoverOutput)
	}
	if ix, ok := sr.Completed["index"]; ok {
		res.Index, _ = ix.Output.(*indexOutput)
	}

	fillRecoveryMetrics(&sum, res)
	fillIndexMetrics(&sum, res)
	sum.P95LatencyMs = computeP95(stream, res)
	sum.BlockedDependency = blockedName(sr)
	sum.ExitCode = decideExitCode(sr, res)
	sum.OutputSHA256 = outputDigest(res)

	res.Summary = sum
	return res, nil
}

func mergeParams(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

var _ = time.Millisecond
