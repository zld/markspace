// Command mediacore is the terminal entry point. It only drives the
// reusable libraries and prints machine-readable JSON results.
//
//	mediacore demo --out DIR [--seed N] [--threshold T] [--tolerance T]
//	mediacore run  --in FILE [--kind mp4|rtp] --out DIR
//
// Exit codes: 0 success, 1 argument error, 2 processing failure.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediacore/internal/fixture"
	"mediacore/internal/maptrack"
	"mediacore/internal/merge"
	"mediacore/internal/model"
	"mediacore/internal/task"
)

const (
	exitOK         = 0
	exitUsage      = 1
	exitProcessing = 2
)

func main() { os.Exit(runCLI(os.Args[1:])) }

func runCLI(args []string) int {
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	switch args[0] {
	case "demo":
		return demoCmd(args[1:])
	case "run":
		return runCmd(args[1:])
	default:
		usage()
		return exitUsage
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mediacore demo --out DIR [--seed N] [--threshold T] [--tolerance T]")
	fmt.Fprintln(os.Stderr, "       mediacore run --in FILE [--kind mp4|rtp] --out DIR")
}

func emit(v interface{}) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

// defaultRules derives the explicit mapping rule set from demuxed tracks.
func defaultRules(tracks []model.Track) []maptrack.Rule {
	var rules []maptrack.Rule
	for _, t := range tracks {
		r := maptrack.Rule{In: t.ID}
		switch t.Kind {
		case model.TrackVideo:
			r.Out = "out:v"
		case model.TrackSubtitle:
			r.Out = "out:s"
		case model.TrackMeta:
			r.Out = "out:m"
		default:
			r.Out = "out:v"
		}
		rules = append(rules, r)
	}
	return rules
}

func newPipeline(storeDir, outDir string, threshold, tolerance int64) *task.Pipeline {
	return &task.Pipeline{
		Store:     task.NewStore(storeDir),
		OutDir:    outDir,
		Threshold: threshold,
		Tolerance: tolerance,
		Outs:      fixture.OutputTracks(),
		RulesFn:   defaultRules,
	}
}

type errorJSON struct {
	Category model.ErrCategory `json:"category"`
	Stage    model.Stage       `json:"stage"`
	TrackID  string            `json:"track_id,omitempty"`
	Offset   int64             `json:"offset"`
	Msg      string            `json:"msg"`
}

func errToJSON(err error) errorJSON {
	if me, ok := err.(*model.Error); ok {
		return errorJSON{Category: me.Category, Stage: me.Stage, TrackID: me.TrackID, Offset: me.Offset, Msg: me.Msg}
	}
	return errorJSON{Category: model.ErrIO, Stage: model.StageDemux, Offset: -1, Msg: err.Error()}
}

// runOne executes the pipeline for a single sample and prints the result.
// Returns the task result (nil on failure) and whether it succeeded.
func runOne(p *task.Pipeline, sampleID, path, kind string) (*task.Result, *model.Error) {
	res, err := p.Run(sampleID, path, kind)
	if err != nil {
		return res, model.AsError(err, model.StageDemux)
	}
	return res, nil
}

func printSuccess(res *task.Result, rules []maptrack.Rule) {
	m := merge.Build([]merge.TaskResult{{TaskKey: res.TaskKey, SampleID: res.SampleID, Events: res.Events}})
	status := "ok"
	if !res.Report.OK {
		status = "deviation"
	}
	emit(map[string]interface{}{
		"sample":             res.SampleID,
		"status":             status,
		"exit_code":          exitOK,
		"task_key":           res.TaskKey,
		"rules":              rules,
		"events":             res.Report.Events,
		"items":              m.Items,
		"event_count":        len(m.Items),
		"max_time_dev_ticks": res.Report.MaxTimeDevTicks,
		"tolerance_ticks":    res.Report.ToleranceTicks,
		"byte_deviation":     res.Report.ByteDeviation,
		"payload_sha256":     res.Report.PayloadSHA256,
		"manifest_digest":    m.Digest,
		"drift":              res.Drift,
		"stages":             res.Notes,
	})
}

func printFailure(sampleID string, err *model.Error) {
	emit(map[string]interface{}{
		"sample":    sampleID,
		"status":    "failed",
		"exit_code": exitProcessing,
		"error":     errToJSON(err),
	})
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	in := fs.String("in", "", "input file")
	kind := fs.String("kind", "", "input kind: mp4 or rtp (default: by extension)")
	out := fs.String("out", "", "work directory")
	threshold := fs.Int64("threshold", 45000, "drift threshold in unified ticks")
	tolerance := fs.Int64("tolerance", 45, "time deviation tolerance in unified ticks")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *in == "" || *out == "" {
		usage()
		return exitUsage
	}
	k := *kind
	if k == "" {
		if filepath.Ext(*in) == ".rtp" {
			k = "rtp"
		} else {
			k = "mp4"
		}
	}
	if k != "mp4" && k != "rtp" {
		usage()
		return exitUsage
	}
	sampleID := filepath.Base(*in)
	p := newPipeline(filepath.Join(*out, "store"), filepath.Join(*out, "out"), *threshold, *tolerance)
	res, perr := runOne(p, sampleID, *in, k)
	if perr != nil {
		printFailure(sampleID, perr)
		return exitProcessing
	}
	tracks := tracksOf(p, res)
	printSuccess(res, defaultRules(tracks))
	if !res.Report.OK {
		return exitProcessing
	}
	return exitOK
}

// tracksOf recovers input tracks from the demux checkpoint for rule display.
func tracksOf(p *task.Pipeline, res *task.Result) []model.Track {
	payload, _, err := p.Store.Load(res.TaskKey, model.StageDemux)
	if err != nil {
		return nil
	}
	var dp struct {
		Tracks []model.Track `json:"tracks"`
	}
	if json.Unmarshal(payload, &dp) != nil {
		return nil
	}
	return dp.Tracks
}
