// Package demo wires parsing, indexing, bounded streaming, rewrap, recovery,
// and evidence output into the terminal acceptance demonstration.
package demo

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediapipeline/internal/index"
	"mediapipeline/internal/recovery"
	"mediapipeline/internal/rtphint"
)

// Run executes one demo invocation against a persistent work directory.
func Run(cfg Config) error {
	cfg.normalize()
	out := cfg.Stdout
	if cfg.Work == "" {
		return errors.New("demo: --work is required")
	}

	dirs := []string{
		filepath.Join(cfg.Work, "input"),
		filepath.Join(cfg.Work, "products"),
		filepath.Join(cfg.Work, "evidence"),
		filepath.Join(cfg.Work, "index"),
		filepath.Join(cfg.Work, "recovery"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	store, err := index.Open(filepath.Join(cfg.Work, "index", "index.json"))
	if err != nil {
		return err
	}
	snapshots, err := index.OpenSnapshots(filepath.Join(cfg.Work, "index", "snapshots.json"))
	if err != nil {
		return err
	}
	journal, err := recovery.Open(filepath.Join(cfg.Work, "recovery", "journal.jsonl"))
	if err != nil {
		return err
	}
	defer journal.Close()

	recoveryStats, err := journal.Recover()
	if err != nil {
		return err
	}
	for _, in := range recoveryStats.RecoveredItems {
		if _, err := store.MarkRecovered(in.Key, in.TargetPath, in.SHA256, in.Size); err != nil &&
			!errors.Is(err, index.ErrAlreadyPublished) && !errors.Is(err, index.ErrNotFound) {
			return err
		}
	}
	for _, in := range recoveryStats.CorruptItems {
		if _, err := recovery.Quarantine(in.TargetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	fmt.Fprintf(out, "recovery recovered=%d republished=%d missing=%d duplicates=%d corrupt=%d\n",
		recoveryStats.Recovered, recoveryStats.Republished, recoveryStats.Missing,
		recoveryStats.Duplicates, recoveryStats.Corrupt)

	files, err := GenerateInputs(filepath.Join(cfg.Work, "input"))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "generated dir=%s normal=%s anomalies=truncated_moov,stco_out_of_bounds,backward_timestamp,container_only\n",
		files.Dir, normalName)

	rtpReport, err := runRTPHintCheck(out)
	if err != nil {
		return err
	}

	crashLeft := cfg.CrashAfterPublish
	result, _, err := processNormal(cfg, files, store, journal, snapshots, &crashLeft)
	if errors.Is(err, ErrInjectedCrash) {
		fmt.Fprintln(out, "injected_crash stage=after_publish exit=86")
		return err
	}
	if err != nil {
		return err
	}
	printNormalMetrics(out, cfg, result)
	fmt.Fprintf(out, "index source=%s decision=%s scope=%s reused_product=%v\n",
		normalName, result.Decision.Reason, result.Decision.Scope, result.Reused)

	corrupt := reportAnomalies(out, cfg, files)
	containerDecision, err := reportContainerOnly(out, cfg, files, snapshots)
	if err != nil {
		return err
	}

	evidencePath := filepath.Join(cfg.Work, "evidence", "demo-report.json")
	if err := writeEvidence(evidencePath, cfg, result, rtpReport, containerDecision, corrupt); err != nil {
		return err
	}
	fmt.Fprintf(out, "evidence path=%s\n", evidencePath)

	if corrupt {
		fmt.Fprintln(out, "result=corrupt_inputs exit=2")
		return ErrCorruptInputs
	}
	fmt.Fprintln(out, "result=pass exit=0")
	return nil
}

func runRTPHintCheck(out io.Writer) (rtphint.SequenceReport, error) {
	seqs := []uint16{65534, 65535, 0, 1, 2, 3}
	raw := make([][]byte, 0, len(seqs))
	for _, seq := range seqs {
		raw = append(raw, rtphint.MustMarshalPacket(seq))
	}
	rep, err := rtphint.ValidateSequence(raw)
	if err != nil {
		return rep, err
	}
	status := "ok"
	if rep.Backwards != 0 {
		status = "backward"
	}
	fmt.Fprintf(out, "rtp_seq_wrap=%s packets=%d wraps=%d last_ext_seq=%d\n",
		status, rep.Packets, rep.WrapCount, rep.LastExtSeq)
	return rep, nil
}
