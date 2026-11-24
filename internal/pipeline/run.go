package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"

	mpierr "mediapipeline/internal/errors"
	"mediapipeline/internal/provenance"
)

// Bounded fixture sizes used by the in-process generator.
const (
	demoPackets      = 40
	demoVideoFrames  = 30
	gapSilenceBytes  = 16
)

// isEOF reports whether the parser reached a clean end of stream.
func isEOF(err error) bool { return errors.Is(err, io.EOF) }

// configKey is the stable configuration digest input for idempotency. It uses
// fixed-width big-endian encoding so the value is identical for a given
// configuration regardless of host integer semantics.
func (e *Engine) configKey() string {
	raw := make([]byte, 16)
	binary.BigEndian.PutUint32(raw[0:4], uint32(e.cfg.JitterWindow))
	binary.BigEndian.PutUint64(raw[4:12], uint64(e.cfg.Seed))
	binary.BigEndian.PutUint64(raw[8:16], e.cfg.SyncToleranceTicks)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// inputDigestOf returns the persisted input digest of a completed node.
func (e *Engine) inputDigestOf(node string) string {
	if st, ok := e.store.Get(node); ok {
		return st.InputDigest
	}
	return ""
}

// Run executes the DAG and converts the scheduler outcome into a Report.
func (e *Engine) Run() Report {
	rep := Report{}
	srep := e.sched.Run()

	for _, r := range srep.Results {
		if r.Reused {
			rep.ReusedNodes = append(rep.ReusedNodes, r.Node)
		}
	}
	rep.BlockedDependency = srep.Blocked
	rep.SyncOffsetTicks = e.syncOffset

	// When the mux node was reused (or rebuilt from durable artifacts), the
	// in-memory digest is empty; recompute it from the existing output so a
	// resumed process reports the same checksum as the original.
	if e.outputDigest == "" {
		e.outputDigest = hashIfExists(e.cfg.OutPath)
	}

	// Load counts from artifacts/checkpoint even when mux was reused or
	// blocked, so the report stays informative on resume.
	e.fillCountsFromState(&rep)

	if srep.FailedErr != nil {
		rep.FirstError = srep.FailedErr
	}

	// Finalize provenance: attach output digest and checkpoint state.
	e.finalizeProvenance()
	digest, _ := e.rec.Digest()
	rep.ProvenanceDigest = digest
	rep.Summary = e.outputDigest
	return rep
}

// hashIfExists returns the SHA-256 of path, or "" when it does not exist.
func hashIfExists(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fillCountsFromState reads durable artifacts so counts survive a new process.
func (e *Engine) fillCountsFromState(rep *Report) {
	var aa audioArtifact
	if err := readGob(e.artifactPath(NodeRecover), &aa); err == nil {
		rep.Packets = len(aa.Samples)
		rep.Gaps = aa.GapCount
		rep.Duplicates = aa.Duplicate
	}
}

// finalizeProvenance rebuilds a canonical entry for every DAG node solely
// from the durable checkpoint. Because the checkpoint is itself idempotent, a
// resumed process (which reused most nodes and therefore produced no fresh
// entries) ends up with the exact same provenance record as the first run.
func (e *Engine) finalizeProvenance() {
	for _, node := range []string{NodeParse, NodeJitter, NodeRecover, NodeSync, NodeMux} {
		st, ok := e.store.Get(node)
		if !ok || st.Status != "done" {
			continue
		}
		en := provenance.Entry{
			Node: node, NodeVersion: provenance.NodeVersion,
			InputDigest: st.InputDigest, ConfigDigest: st.ConfigDigest,
			CheckpointState: "done",
		}
		// Only the mux node produces an externally verifiable output digest.
		if node == NodeMux && e.outputDigest != "" {
			en.OutputDigest = e.outputDigest
		}
		e.rec.Add(en)
	}
}

// firstErrorOffset extracts the byte offset from a media error, or -1.
func firstErrorOffset(e *mpierr.MediaError) int64 {
	if e == nil {
		return -1
	}
	return e.ByteOffset
}
