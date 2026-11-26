// Command mediarc maps and merges media archive versions from local raw
// streams and keyframe indexes, repairing clock wraparound and source
// switches, committing per-version transactions with rollback on conflict,
// and printing a stable report. Terminal-only; no external services.
//
// Exit codes: 0 = ran (corrupt samples still produce a report),
// 1 = bad arguments or unreadable input, 2 = archive unrecoverable.
package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediarc/internal/archive"
	"mediarc/internal/clock"
	"mediarc/internal/fixture"
	"mediarc/internal/index"
	"mediarc/internal/merge"
	"mediarc/internal/model"
	"mediarc/internal/pipeline"
	"mediarc/internal/report"
	"mediarc/internal/scan"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "demo":
		return demo(args[1:])
	case "check":
		return check(args[1:])
	default:
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediarc demo --seed N --out DIR --memory BYTES")
	fmt.Fprintln(os.Stderr, "       mediarc check --out DIR")
}

func codecOf(name string) scan.Codec {
	switch filepath.Ext(name) {
	case ".h265":
		return scan.CodecH265
	case ".av1":
		return scan.CodecAV1
	default:
		return scan.CodecH264
	}
}

func scanStream(data []byte, codec scan.Codec) ([]scan.AU, *model.Error) {
	if codec == scan.CodecAV1 {
		return scan.ScanAV1(data)
	}
	return scan.ScanNAL(data, codec)
}

func demo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Int64("seed", 0, "fixture seed")
	out := fs.String("out", "", "output directory")
	memory := fs.Int64("memory", 0, "pipeline cache limit in bytes")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *out == "" || *memory <= 0 {
		usage()
		return 1
	}
	// Reset the run directory so repeated runs are identical.
	if err := os.RemoveAll(*out); err != nil {
		return fail(model.ErrInput, err)
	}
	inputDir := filepath.Join(*out, "input")
	if merr := fixture.Generate(inputDir, *seed); merr != nil {
		return fail(merr.Code, fmt.Errorf("%s", merr.Msg))
	}
	store, merr := archive.Open(filepath.Join(*out, "archive"))
	if merr != nil {
		if merr.Code == model.ErrArchive {
			fmt.Fprintln(os.Stderr, "archive unrecoverable:", merr.Msg)
			return 2
		}
		return fail(merr.Code, fmt.Errorf("%s", merr.Msg))
	}
	rep := &report.Report{}
	rep.Summary.MemoryLimit = *memory

	names, err := readDirSorted(inputDir)
	if err != nil {
		return fail(model.ErrInput, err)
	}
	referenced := map[string]bool{}
	var samples []merge.Sample
	versions := map[string]bool{}
	for _, name := range names {
		if !strings.HasSuffix(name, ".index.json") {
			continue
		}
		var idx index.File
		data, err := os.ReadFile(filepath.Join(inputDir, name))
		if err != nil || json.Unmarshal(data, &idx) != nil {
			return fail(model.ErrInput, fmt.Errorf("unreadable index %s", name))
		}
		streamPath, perr := archive.SafeJoin(inputDir, idx.Stream)
		if perr != nil {
			return fail(perr.Code, fmt.Errorf("%s", perr.Msg))
		}
		stream, err := os.ReadFile(streamPath)
		if err != nil {
			return fail(model.ErrInput, err)
		}
		referenced[idx.Stream] = true
		aus, serr := scanStream(stream, codecOf(idx.Stream))
		if serr != nil {
			rep.Errors = append(rep.Errors, report.ErrorEntry{Stage: "scan", Code: serr.Code, Offset: serr.Offset, Msg: serr.Msg})
		}
		bounds := map[int64]bool{}
		for _, a := range aus {
			bounds[a.Offset] = true
		}
		valid, errs := index.Validate(idx.Records, int64(len(stream)), bounds)
		for _, e := range errs {
			rep.Errors = append(rep.Errors, report.ErrorEntry{Stage: "index", Code: e.Code, Track: e.Track, Offset: e.Offset, Msg: e.Msg})
		}
		var clockIn []clock.Sample
		for _, v := range valid {
			if v.HasRTP {
				clockIn = append(clockIn, clock.Sample{SrcOffset: v.Offset, Raw: v.RTPTimestamp})
			}
		}
		for _, r := range clock.Repair(clockIn) {
			if r.Event != "" {
				rep.Events = append(rep.Events, report.Event{Track: 1, Offset: r.SrcOffset, Kind: r.Event, Raw: r.Raw, Delta: r.Delta})
			}
		}
		had := false
		for _, v := range valid {
			sum := sha1.Sum(stream[v.Offset : v.Offset+v.Length])
			samples = append(samples, merge.Sample{
				Version: idx.Version, Track: v.Track, Ticks: v.PTS, Scale: v.Timescale,
				SrcOffset: v.Offset, Length: v.Length, Hash: hex.EncodeToString(sum[:8]),
			})
			had = true
		}
		if had {
			versions[idx.Version] = true
		}
	}
	// Orphan streams (no index): scan only, errors still reported.
	for _, name := range names {
		if referenced[name] || !strings.HasSuffix(name, ".h264") && !strings.HasSuffix(name, ".h265") && !strings.HasSuffix(name, ".av1") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(inputDir, name))
		if err != nil {
			return fail(model.ErrInput, err)
		}
		if _, serr := scanStream(data, codecOf(name)); serr != nil {
			rep.Errors = append(rep.Errors, report.ErrorEntry{Stage: "scan", Code: serr.Code, Offset: serr.Offset, Msg: serr.Msg})
		}
	}

	// Bounded pipeline: samples flow through the byte-limited queue.
	var flowed []merge.Sample
	stats := pipeline.Run(pipeline.Config{Memory: *memory},
		func(push func(pipeline.Item)) {
			for _, s := range samples {
				push(pipeline.Item{SizeBytes: s.Length, Value: s})
			}
		},
		func(it pipeline.Item) { flowed = append(flowed, it.Value.(merge.Sample)) })
	rep.Summary.PeakBytes = stats.PeakBytes

	vlist := sortedKeys(versions)
	res := merge.Map(flowed, vlist)
	rep.Items = res.Items
	rep.Summary.Mapped, rep.Summary.Missing, rep.Summary.Conflicts = res.Mapped, res.Missing, res.Conflicts

	// Archive per version; conflicting versions are rolled back.
	conflictV := map[string]bool{}
	for _, it := range res.Items {
		if !it.Conflict {
			continue
		}
		for _, e := range it.Entries {
			if e.Version != it.Chosen {
				conflictV[e.Version] = true
			}
		}
	}
	for _, v := range vlist {
		txn := store.Begin(v)
		if conflictV[v] {
			if merr := txn.Rollback(); merr != nil {
				return fail(merr.Code, fmt.Errorf("%s", merr.Msg))
			}
			continue
		}
		for _, it := range res.Items {
			for _, e := range it.Entries {
				if e.Version == v && e.OutOff >= 0 {
					txn.Add(archive.Entry{Version: v, Track: it.Track, Ticks: it.Ticks, Scale: it.Scale, OutOff: e.OutOff, Length: lengthOf(flowed, v, e), Hash: e.Hash})
				}
			}
		}
		if merr := txn.Commit(); merr != nil {
			return fail(merr.Code, fmt.Errorf("%s", merr.Msg))
		}
	}
	rep.Summary.Rollback = store.Rollbacks()

	rep.Finalize()
	printItems(res)
	for _, e := range rep.Errors {
		fmt.Printf("error stage=%s code=%s track=%d offset=%d %s\n", e.Stage, e.Code, e.Track, e.Offset, e.Msg)
	}
	s := rep.Summary
	fmt.Printf("summary mapped=%d missing=%d conflicts=%d rollback=%d first_error=%d peak_bytes=%d\n",
		s.Mapped, s.Missing, s.Conflicts, s.Rollback, s.FirstError, s.PeakBytes)
	f, err := os.Create(filepath.Join(*out, "report.json"))
	if err != nil {
		return fail(model.ErrInput, err)
	}
	defer f.Close()
	if err := report.Write(f, rep); err != nil {
		return fail(model.ErrInput, err)
	}
	return 0
}

func check(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	out := fs.String("out", "", "output directory")
	if err := fs.Parse(args); err != nil || *out == "" {
		usage()
		return 1
	}
	store, merr := archive.Open(filepath.Join(*out, "archive"))
	if merr != nil {
		fmt.Fprintln(os.Stderr, "archive unrecoverable:", merr.Msg)
		return 2
	}
	fmt.Printf("archive ok: entries=%d rollbacks=%d\n", len(store.Entries()), store.Rollbacks())
	return 0
}

func lengthOf(samples []merge.Sample, version string, e merge.Entry) int64 {
	for _, s := range samples {
		if s.Version == version && s.SrcOffset == e.InOffset {
			return s.Length
		}
	}
	return 0
}

func printItems(res *merge.Result) {
	for _, it := range res.Items {
		fmt.Printf("map track=%d ticks=%d/%d chosen=%s conflict=%v missing=%v\n",
			it.Track, it.Ticks, it.Scale, it.Chosen, it.Conflict, it.Missing)
		for _, e := range it.Entries {
			fmt.Printf("  %s in=%d out=%d dt=%d byte_diff=%d hash=%s\n",
				e.Version, e.InOffset, e.OutOff, e.Dt, e.ByteDiff, e.Hash)
		}
	}
}

func readDirSorted(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fail(code model.Code, err error) int {
	fmt.Fprintf(os.Stderr, "error %s: %v\n", code, err)
	return 1
}
