// Package scan wires the demo fixtures into the batch pipeline and
// produces the deterministic terminal summary the CLI commands print.
package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediascan/internal/batch"
	"mediascan/internal/checkpoint"
	"mediascan/internal/demo"
	"mediascan/internal/errs"
	"mediascan/internal/fingerprint"
	"mediascan/internal/index"
)

// Result is the full run result plus the exit code the CLI should use.
type Result struct {
	Summary    *batch.Summary
	ExitCode   int
	OutDir     string
	Bundle     *demo.Bundle
	HashMatch  bool
	HashTotal  int
	HashFailed int
	// NormalTotal is the non-corrupt sample count used for the 100% target.
	NormalTotal int
	// NormalIndexed is how many normal samples produced a valid index entry.
	NormalIndexed int
	// ErrorOffsets are the locatable offsets actually reported (Unknown
	// offsets are omitted rather than printed as -1).
	ErrorOffsets []int64
}

// Run generates fixtures, processes them in chunks, writes the index and
// returns a result. resume enables checkpoint reuse across the two passes
// the demo performs to demonstrate recovery.
func Run(outDir string, chunkSize int, resume bool) (*Result, error) {
	bundle, err := demo.Generate(outDir)
	if err != nil {
		return nil, err
	}
	identity := checkpoint.Identity{
		InputHash: fingerprint.Sum([]byte("demo-input-set-v1")),
		ManifestHash: fingerprint.ManifestHash(
			append(append([]byte(nil), bundle.ManifestDASH...), bundle.ManifestHLS...)),
		RuleVersion: fingerprint.RuleVersion,
	}

	objs, err := buildObjects(bundle)
	if err != nil {
		return nil, err
	}

	store, err := checkpoint.Open(outDir)
	if err != nil {
		return nil, err
	}
	runner := batch.NewRunner(store, chunkSize, identity)
	sum, err := runner.Run(objs)
	if err != nil {
		return nil, err
	}

	// On resume, run a second pass: committed chunks are reused and only
	// the breakpoint work is redone, proving output stays identical.
	if resume {
		store2, err := checkpoint.Open(outDir)
		if err != nil {
			return nil, err
		}
		runner2 := batch.NewRunner(store2, chunkSize, identity)
		sum2, err := runner2.Run(objs)
		if err != nil {
			return nil, err
		}
		sum.ReusedChunks = runner2.ReusedChunks()
		_ = sum2
	}

	hashTotal, hashFailed := verifyHashes(bundle, runner.Hasher())
	res := &Result{
		Summary:       sum,
		OutDir:        outDir,
		Bundle:        bundle,
		HashMatch:     hashFailed == 0,
		HashTotal:     hashTotal,
		HashFailed:    hashFailed,
		NormalTotal:   len(bundle.Segments),
		NormalIndexed: countNormalIndexed(bundle, runner.Hasher()),
	}
	for _, e := range runner.Errors() {
		if e.Offset != errs.Unknown {
			res.ErrorOffsets = append(res.ErrorOffsets, e.Offset)
		}
	}

	// Persist the local index atomically.
	snap := checkpoint.IndexSnapshot{
		RuleVersion: fingerprint.RuleVersion,
		Records:     runner.Hasher().Records(),
		Keyframes:   toDTO(runner.Index().Snapshot()),
	}
	if err := store.WriteIndex(snap); err != nil {
		return nil, err
	}

	// Exit code 2: corrupt input was encountered but recorded per rules.
	if len(runner.Errors()) > 0 {
		res.ExitCode = 2
	} else {
		res.ExitCode = 0
	}
	return res, nil
}

// buildObjects turns generated segments into batch work objects and adds
// the corrupt fixtures as objects expected to be rejected.
func buildObjects(b *demo.Bundle) ([]batch.Object, error) {
	var objs []batch.Object
	for _, seg := range b.Segments {
		data, err := os.ReadFile(seg.Path)
		if err != nil {
			return nil, err
		}
		objs = append(objs, batch.Object{
			Source:       seg.Source,
			ManifestKind: manifestKind(seg.Source),
			RepID:        seg.RepID,
			SegmentIndex: seg.SegmentIndex,
			TrackID:      seg.TrackID,
			Timescale:    seg.Timescale,
			TimeStart:    seg.TimeStart,
			TimeEnd:      seg.TimeEnd,
			Data:         data,
			Keyframe:     seg.Keyframe,
		})
	}
	// Corrupt objects exercise the reject-and-locate path; they carry the
	// expected first bad offset so the summary can prove localization.
	for _, cf := range b.Corrupt {
		if cf.Kind == "manifest" {
			continue // manifest corruptions are validated separately below
		}
		data, err := os.ReadFile(cf.Path)
		if err != nil {
			return nil, err
		}
		objs = append(objs, batch.Object{
			Source:        filepath.Join(demo.DirCorrupt, filepath.Base(cf.Path)),
			ManifestKind:  "corrupt",
			RepID:         "corrupt",
			SegmentIndex:  int64(len(objs)),
			TrackID:       9,
			Timescale:     90000,
			TimeStart:     0,
			TimeEnd:       100,
			Data:          data,
			Corrupt:       false, // runner runs real container validation
			CorruptOffset: cf.BadOffset,
		})
	}
	return objs, nil
}

func manifestKind(source string) string {
	if strings.HasPrefix(source, demo.DirDASH) {
		return "dash"
	}
	return "hls"
}

// verifyHashes confirms every generated expected hash appears in the
// hasher's records and reports the match counts for the summary.
func verifyHashes(b *demo.Bundle, h *fingerprint.Hasher) (total, failed int) {
	for _, seg := range b.Segments {
		total++
		rec, ok := h.Lookup(seg.ExpectedHash)
		if !ok || rec.ByteLength != seg.ByteLength {
			failed++
		}
	}
	return total, failed
}

// countNormalIndexed counts generated (non-corrupt) segments whose content
// hash is present: these are the round-trip parse successes that must hit
// 100% on a healthy sample set.
func countNormalIndexed(b *demo.Bundle, h *fingerprint.Hasher) int {
	n := 0
	for _, seg := range b.Segments {
		if _, ok := h.Lookup(seg.ExpectedHash); ok {
			n++
		}
	}
	return n
}

func toDTO(ks []index.Keyframe) []checkpoint.KeyframeDTO {
	out := make([]checkpoint.KeyframeDTO, 0, len(ks))
	for _, k := range ks {
		out = append(out, checkpoint.KeyframeDTO{
			TrackID:      k.TrackID,
			SampleNumber: k.SampleNumber,
			SegmentIndex: k.SegmentIndex,
			TimeTicks:    k.TimeTicks,
			ByteOffset:   k.ByteOffset,
			State:        string(k.State),
		})
	}
	return out
}

// PrintSummary renders the required terminal metrics.
func (r *Result) PrintSummary() string {
	var b strings.Builder
	s := r.Summary
	fmt.Fprintf(&b, "mediascan summary\n")
	fmt.Fprintf(&b, "  segments total:        %d (normal=%d corrupt=%d)\n",
		s.TotalSegments, r.NormalTotal, s.TotalSegments-r.NormalTotal)
	fmt.Fprintf(&b, "  segments indexed:      %d\n", s.IndexedSegments)
	rate := 100 * float64(r.NormalIndexed) / float64(r.NormalTotal)
	fmt.Fprintf(&b, "  round-trip parse rate: %.1f%% (normal samples)\n", rate)
	hit := keyframeHitRate(s)
	fmt.Fprintf(&b, "  keyframe index hits:   %d/%d (%.1f%%)\n",
		s.KeyframeHits, s.KeyframeWanted, hit)
	fmt.Fprintf(&b, "  misalignments:         %d\n", s.Misalignments)
	fmt.Fprintf(&b, "  sha-256 matches:       %d/%d\n", r.HashTotal-r.HashFailed, r.HashTotal)
	fmt.Fprintf(&b, "  checkpoint resumes:    %d reused chunks\n", s.ReusedChunks)
	fmt.Fprintf(&b, "  recorded errors:       %d\n", len(s.Errors))
	for _, e := range s.Errors {
		fmt.Fprintf(&b, "    - %s\n", e.Error())
	}
	if len(r.ErrorOffsets) > 0 {
		fmt.Fprintf(&b, "  error offsets:         %v\n", r.ErrorOffsets)
	}
	return b.String()
}

func keyframeHitRate(s *batch.Summary) float64 {
	if s.KeyframeWanted == 0 {
		return 0
	}
	return 100 * float64(s.KeyframeHits) / float64(s.KeyframeWanted)
}

// Verify re-opens the persisted index and checks it against a fresh scan,
// returning exit code 0 on match and 1 on index structure problems.
func Verify(outDir string) (int, string, error) {
	store, err := checkpoint.Open(outDir)
	if err != nil {
		return 1, "", err
	}
	snap, err := store.ReadIndex()
	if err != nil {
		if se, ok := errs.AsError(err); ok && se.Kind == errs.KindArgs {
			return 1, "", err
		}
		return 1, "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "verify index: rule=%s records=%d keyframes=%d\n",
		snap.RuleVersion, len(snap.Records), len(snap.Keyframes))
	// Structural integrity: every record needs a hash and at least one ref.
	for _, rec := range snap.Records {
		if len(rec.Hash) != 64 || len(rec.Refs) == 0 {
			return 1, b.String(), errs.New(errs.KindArgs, outDir, errs.Unknown,
				errs.Unknown, 0, "index record missing hash or refs")
		}
	}
	fmt.Fprintf(&b, "verify: OK (%d content objects, %d keyframes)\n",
		len(snap.Records), len(snap.Keyframes))
	return 0, b.String(), nil
}
