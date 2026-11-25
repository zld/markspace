package main

import (
	"os"
	"path/filepath"

	"assetcheck/internal/cache"
	"assetcheck/internal/errmodel"
	"assetcheck/internal/index"
	"assetcheck/internal/stream"
	"assetcheck/internal/txn"
	"assetcheck/internal/wav"
)

func exitCodeFor(err error) int { return errmodel.ExitCode(err) }

func runAnalyze(opts options) (*ReportView, error) {
	absPath, _ := filepath.Abs(opts.input)
	data, err := os.ReadFile(opts.input)
	if err != nil {
		return nil, errmodel.New(errmodel.ErrReadInput, "cannot read input %q: %v", opts.input, err)
	}

	store, err := index.OpenStore(opts.indexDir)
	if err != nil {
		return nil, err
	}
	mgr, err := txn.NewManager(opts.indexDir)
	if err != nil {
		return nil, err
	}
	defer mgr.Close()

	// Every entry performs recovery first; -resume makes the intent explicit.
	recovery, err := mgr.Recover(store)
	if err != nil {
		return nil, err
	}
	cacheStore, err := cache.OpenInDir(opts.indexDir)
	if err != nil {
		return nil, err
	}

	var invalidations []cache.Invalidation
	mutationLabel := ""
	if opts.mutate != "" {
		mutationLabel = opts.mutate
		data, invalidations, err = runMutation(absPath, opts.mutate, data, cacheStore)
		if err != nil {
			return nil, err
		}
	}

	w, perr := wav.Parse(data)
	if perr != nil {
		return &ReportView{
			File: opts.input, Mutation: mutationLabel, EvalPass: false,
			Recovery: &recovery,
		}, perr
	}
	fp := fingerprintsOf(data, w)

	if opts.mutate == "" {
		if old, ok := cacheStore.Get(absPath); ok {
			invalidations = cache.Check(old, true, fp)
		}
	}

	rep, err := stream.Scan(w, data, stream.Budget{})
	if err != nil {
		return nil, err
	}
	ev := stream.Evaluate(rep, opts.thresholds.t)
	digest := stream.DigestSummary(fp.Content, rep, ev)

	recs, _ := mgr.WAL().ReadAll()
	lastSeq := txn.LastCommitSeq(recs)
	txid, seq, err := mgr.Begin()
	if err != nil {
		return nil, err
	}
	_ = mgr.Stage(txid, "scan", txn.Checkpoint([]byte(digest)))
	_ = cacheStore.Put(cache.Entry{Path: absPath, Fingerprints: fp, SummaryDigest: digest})
	_ = mgr.Stage(txid, "cache-update", txn.Checkpoint([]byte(fp.Content+"|"+fp.Structure+"|"+fp.Metadata)))

	idx, err := index.Build(rep, fp.Content, fp.Metadata)
	if err != nil {
		return nil, err
	}
	artifact := store.NewArtifact(txid, fp.Content, seq)
	if err := store.WriteStaging(&artifact, idx); err != nil {
		return nil, err
	}
	if err := mgr.RecordPrepared(artifact); err != nil {
		return nil, err
	}
	if err := mgr.Stage(txid, "index-prepare", txn.Checkpoint([]byte(idx.Digest))); err != nil {
		return nil, err
	}

	view := buildView(opts, w, rep, ev, digest, fp, invalidations, recovery)
	view.Mutation = mutationLabel
	view.Txn.TxnID = txid
	view.Txn.LastCommitSeq = lastSeq
	view.Txn.PreparedSeq = seq
	view.Index = IndexView{
		Dir: opts.indexDir, ContentKey: index.ContentKey(fp.Content),
		Digest: idx.Digest, Keys: len(idx.Keys),
	}

	if opts.injectFailure != "" {
		if opts.injectFailure != "after-index-prepare" {
			return view, errmodel.New(errmodel.ErrInjected, "unknown injection point %q", opts.injectFailure)
		}
		view.Txn.InjectedPoint = opts.injectFailure
		return view, errmodel.New(errmodel.ErrInjected,
			"injected failure at %s: txn=%s prepared-seq=%d left uncommitted; run with -resume to recover",
			opts.injectFailure, txid, seq)
	}

	if err := mgr.Commit(artifact, store); err != nil {
		return view, err
	}
	_ = cacheStore.Put(cache.Entry{
		Path: absPath, Fingerprints: fp, SummaryDigest: digest, IndexSeq: seq,
	})
	view.Txn.CommitSeq = seq
	if visible, e, verr := store.Visible(fp.Content); verr == nil {
		view.Index.VisibleSeq = e.Seq
		view.Index.Digest = visible.Digest
		view.Index.Keys = len(visible.Keys)
	}
	return view, nil
}

func buildView(
	opts options,
	w *wav.Wav,
	rep *stream.Report,
	ev *stream.Evaluation,
	digest string,
	fp cache.Fingerprints,
	inv []cache.Invalidation,
	recovery txn.RecoveryResult,
) *ReportView {
	items := ev.Items
	v := &ReportView{
		File: opts.input, SummaryDigest: digest, Fingerprints: fp,
		Audio: FrameInfo{
			TotalFrames: rep.TotalFrames, PresentFrames: rep.PresentFrames,
			Channels: rep.Channels, SampleRate: rep.SampleRate,
			BitsPerSample: w.Config.BitsPerSample, BlockAlign: w.Config.BlockAlign,
			DurationSeconds: float64(rep.Clock.FramesToNanos(rep.TotalFrames)) / 1e9,
		},
		Interval: rep.Interval, CorruptRanges: rep.CorruptRanges,
		CorruptFrames: rep.CorruptFrames, CorruptRatio: rep.CorruptRatio,
		BudgetExceeded: rep.BudgetExceeded, LastFrame: rep.LastFrame,
		Findings: rep.Findings, Items: items, EvalPass: ev.Pass,
		Invalidations: inv, Recovery: &recovery,
	}
	return v
}
