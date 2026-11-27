package txn

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediacheck/internal/index"
	"mediacheck/internal/manifest"
	"mediacheck/internal/provenance"
)

// ErrSimulatedCrash is returned when a run is deliberately interrupted at a
// stage boundary (used to produce the interrupted fixture state).
var ErrSimulatedCrash = errors.New("txn: simulated crash")

// Outcome is the result of a pipeline run.
type Outcome struct {
	Result        *provenance.Result
	Recovery      *Recovery
	IdentityState string // "reused" or "rebuilt"
	Invalidation  string // why a previous result was invalidated
}

// RunPipeline executes parse→map→build→commit with WAL journaling inside
// out. Completed stages are reused from their journaled outputs, so a
// restarted run resumes from the last valid stage and never produces
// duplicate index entries. crashAt simulates an interruption after the
// "begin" record of the named stage.
func RunPipeline(out, format string, crashAt string) (*Outcome, error) {
	w, err := OpenWAL(out)
	if err != nil {
		return nil, err
	}
	tx := &Txn{Dir: out, WAL: w}
	rec, err := tx.Recover()
	if err != nil {
		return nil, err
	}
	inputs, err := inputFiles(out, format)
	if err != nil {
		return nil, err
	}
	id, err := provenance.ComputeIdentity(out, inputs, provenance.ConfigDigest("format="+format))
	if err != nil {
		return nil, err
	}
	oc := &Outcome{Recovery: rec, IdentityState: "rebuilt", Invalidation: "none"}
	if w.Done(StageCommit) {
		res, lerr := provenance.LoadResult(out)
		if lerr == nil && res.Identity.Digest() == id.Digest() {
			oc.Result = res
			oc.IdentityState = "reused"
			return oc, nil
		}
		if lerr == nil {
			oc.Invalidation = provenance.DiffInputs(res.Identity, id)
		} else {
			oc.Invalidation = "committed result unreadable"
		}
		if err := w.Reset(); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}

	var events []manifest.Event
	if w.Done(StageParse) {
		data, err := tx.ReadStage(StageParse)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &events); err != nil {
			return nil, err
		}
	} else {
		if err := w.Log(StageParse, "begin", ""); err != nil {
			return nil, err
		}
		events, err = ParseAll(out, format)
		if err != nil {
			return nil, err
		}
		if err := tx.WriteStage(StageParse, mustJSON(events)); err != nil {
			return nil, err
		}
		if err := w.Log(StageParse, "done", fmt.Sprintf("events=%d", len(events))); err != nil {
			return nil, err
		}
	}
	if crashAt == StageParse {
		return nil, ErrSimulatedCrash
	}

	var entries []index.Entry
	if w.Done(StageMap) {
		data, err := tx.ReadStage(StageMap)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, err
		}
	} else {
		if err := w.Log(StageMap, "begin", ""); err != nil {
			return nil, err
		}
		entries, err = index.MapEvents(events)
		if err != nil {
			return nil, err
		}
		if err := tx.WriteStage(StageMap, mustJSON(entries)); err != nil {
			return nil, err
		}
		if err := w.Log(StageMap, "done", fmt.Sprintf("entries=%d", len(entries))); err != nil {
			return nil, err
		}
	}
	if crashAt == StageMap {
		return nil, ErrSimulatedCrash
	}

	var ix *index.Index
	if w.Done(StageBuild) {
		data, err := tx.ReadStage(StageBuild)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &ix); err != nil {
			return nil, err
		}
	} else {
		if err := w.Log(StageBuild, "begin", ""); err != nil {
			return nil, err
		}
		if crashAt == StageBuild {
			// leave a torn temp index behind, as an interrupted write would
			if err := tx.WriteStage(StageBuild, []byte(`{"partial":`)); err != nil {
				return nil, err
			}
			return nil, ErrSimulatedCrash
		}
		ix, err = index.Assemble(out, entries)
		if err != nil {
			return nil, err
		}
		if err := tx.WriteStage(StageBuild, mustJSON(ix)); err != nil {
			return nil, err
		}
		if err := w.Log(StageBuild, "done", fmt.Sprintf("anomalies=%d", len(ix.Anomalies))); err != nil {
			return nil, err
		}
	}

	if err := w.Log(StageCommit, "begin", ""); err != nil {
		return nil, err
	}
	res := &provenance.Result{Identity: id, Events: len(events), Index: ix}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := tx.CommitAtomic(provenance.ResultFile, data); err != nil {
		return nil, err
	}
	if err := w.Log(StageCommit, "done", "digest="+provenance.DigestIndex(ix)); err != nil {
		return nil, err
	}
	oc.Result = res
	return oc, nil
}

// CleanDigest rebuilds the index purely in memory (no WAL, no writes) and
// returns its digest, for comparing a recovered result with a clean build.
func CleanDigest(out, format string) (string, error) {
	events, err := ParseAll(out, format)
	if err != nil {
		return "", err
	}
	entries, err := index.MapEvents(events)
	if err != nil {
		return "", err
	}
	ix, err := index.Assemble(out, entries)
	if err != nil {
		return "", err
	}
	return provenance.DigestIndex(ix), nil
}

// ParseAll parses the manifests selected by format ("hls", "dash", "both")
// and rewrites event paths relative to out so committed results are
// location-independent.
func ParseAll(out, format string) ([]manifest.Event, error) {
	var events []manifest.Event
	rel := func(p string) (string, error) {
		return filepath.Rel(out, p)
	}
	parse := func(p string) ([]manifest.Event, error) {
		full := filepath.Join(out, p)
		var evs []manifest.Event
		var err error
		if strings.HasSuffix(p, ".m3u8") {
			evs, err = manifest.ParseHLS(full)
		} else {
			evs, err = manifest.ParseDASH(full)
		}
		if err != nil {
			return nil, err
		}
		for i := range evs {
			if evs[i].ManifestPath, err = rel(evs[i].ManifestPath); err != nil {
				return nil, err
			}
			if evs[i].SegmentPath, err = rel(evs[i].SegmentPath); err != nil {
				return nil, err
			}
		}
		return evs, nil
	}
	if format == "hls" || format == "both" {
		evs, err := parse("fixtures/hls/playlist.m3u8")
		if err != nil {
			return nil, err
		}
		events = append(events, evs...)
	}
	if format == "dash" || format == "both" {
		evs, err := parse("fixtures/dash/manifest.mpd")
		if err != nil {
			return nil, err
		}
		events = append(events, evs...)
	}
	return events, nil
}

// inputFiles lists the root-relative identity inputs: manifests plus all
// segment files under fixtures.
func inputFiles(out, format string) ([]string, error) {
	var inputs []string
	if format == "hls" || format == "both" {
		inputs = append(inputs, "fixtures/hls/playlist.m3u8")
	}
	if format == "dash" || format == "both" {
		inputs = append(inputs, "fixtures/dash/manifest.mpd")
	}
	root := filepath.Join(out, "fixtures")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".m4s") || strings.HasSuffix(p, ".mp4") {
			r, err := filepath.Rel(out, p)
			if err != nil {
				return err
			}
			inputs = append(inputs, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(inputs)
	return inputs, nil
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// Ensure os import is used (stage files are read via Txn).
var _ = os.Open
