// Command continuity reconstructs auditable continuity state from local
// HLS/DASH manifests, fMP4 segments and RTP event records.
//
// Usage:
//
//	continuity demo --case normal|damaged|recover --seed N
//	continuity run --manifest <m3u8|mpd> --events <file> --arrivals <file>
//
// Exit codes: 0 ok; 1 usage error; 2 damaged media (parse stage);
// 3 manifest/input error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"continuity/internal/adapter"
	"continuity/internal/core"
	"continuity/internal/fixture"
	"continuity/internal/manifest"
	"continuity/internal/metrics"
)

const (
	exitOK = iota
	exitUsage
	exitDamaged
	exitInput
)

func main() { os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr)) }

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "demo":
		fs := flag.NewFlagSet("demo", flag.ContinueOnError)
		fs.SetOutput(stderr)
		caseName := fs.String("case", "", "normal|damaged|recover")
		seed := fs.Int64("seed", 1, "fixture seed")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		switch *caseName {
		case "normal", "damaged", "recover":
		default:
			fmt.Fprintln(stderr, "error: --case must be normal, damaged or recover")
			return exitUsage
		}
		dir, err := os.MkdirTemp("", "continuity-demo-")
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitInput
		}
		defer os.RemoveAll(dir)
		if err := fixture.Generate(dir, *caseName, *seed); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitInput
		}
		return runPipeline(filepath.Join(dir, "index.m3u8"),
			filepath.Join(dir, "events.rtp"), filepath.Join(dir, "arrivals.tsv"), stdout, stderr)
	case "run":
		fs := flag.NewFlagSet("run", flag.ContinueOnError)
		fs.SetOutput(stderr)
		mani := fs.String("manifest", "", "local .m3u8 or .mpd path")
		events := fs.String("events", "", "RTP event records file")
		arrivals := fs.String("arrivals", "", "arrival records file")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if *mani == "" || *events == "" || *arrivals == "" {
			usage(stderr)
			return exitUsage
		}
		return runPipeline(*mani, *events, *arrivals, stdout, stderr)
	default:
		usage(stderr)
		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  continuity demo --case normal|damaged|recover --seed N")
	fmt.Fprintln(w, "  continuity run --manifest <m3u8|mpd> --events <file> --arrivals <file>")
}

// runPipeline executes manifest -> parse -> timebase -> kernel -> report.
func runPipeline(maniPath, eventsPath, arrivalsPath string, stdout, stderr io.Writer) int {
	pl, err := parseManifest(maniPath)
	if err != nil {
		return fail(stderr, err)
	}
	arrivals, err := readArrivals(arrivalsPath)
	if err != nil {
		return fail(stderr, err)
	}
	events, err := readEvents(eventsPath)
	if err != nil {
		return fail(stderr, err)
	}
	parser, _ := adapter.Get("mp4")
	in := core.Input{Events: events}
	for i, seg := range pl.Segments {
		src := seg.URI
		arr, ok := arrivals[src]
		if !ok {
			return fail(stderr, core.NewError(core.StageManifest, core.CodeInput, maniPath, seg.Line, -1,
				"no arrival record for "+src, nil))
		}
		info, perr := parseSegmentFile(parser, filepath.Join(pl.Dir, src), src)
		if perr != nil {
			var ce *core.Error
			if errors.As(perr, &ce) {
				ce.Msg = fmt.Sprintf("segment[%d] %s", i, ce.Msg)
			}
			return fail(stderr, perr)
		}
		in.Records = append(in.Records, core.Record{
			Index: seg.Index, Source: src, SourceID: arr.sourceID,
			Line: seg.Line, ArrivalRef: arr.at, Info: info,
		})
	}
	res, err := core.Build(in)
	if err != nil {
		return fail(stderr, err)
	}
	metrics.Report(stdout, res)
	return exitOK
}

func parseManifest(path string) (*manifest.Playlist, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m3u8":
		return manifest.ParseHLS(path)
	case ".mpd":
		return manifest.ParseDASH(path)
	}
	return nil, core.NewError(core.StageManifest, core.CodeInput, path, 0, -1,
		"unsupported manifest extension", nil)
}

func parseSegmentFile(parser adapter.ParserV1, path, src string) (*core.SegmentInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, core.NewError(core.StageParse, core.CodeInput, src, 0, -1, "open", err)
	}
	defer f.Close()
	return parser.ParseSegment(f, src)
}

type arrivalRec struct {
	sourceID string
	at       int64
}

// readArrivals parses "uri<TAB>source_id<TAB>arrival_ref" lines ('#' = comment).
func readArrivals(path string) (map[string]arrivalRec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, core.NewError(core.StageManifest, core.CodeInput, path, 0, -1, "open", err)
	}
	out := map[string]arrivalRec{}
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			return nil, core.NewError(core.StageManifest, core.CodeBadManifest, path, n+1, -1,
				"want 3 tab-separated fields", nil)
		}
		at, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			return nil, core.NewError(core.StageManifest, core.CodeBadManifest, path, n+1, -1,
				"bad arrival ref", err)
		}
		out[f[0]] = arrivalRec{sourceID: f[1], at: at}
	}
	return out, nil
}

func readEvents(path string) ([]core.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, core.NewError(core.StageParse, core.CodeInput, path, 0, -1, "open", err)
	}
	defer f.Close()
	return (adapter.RTPParser{}).ParseEvents(f, path)
}

// fail prints the error and maps it to an exit code.
func fail(stderr io.Writer, err error) int {
	var ce *core.Error
	if errors.As(err, &ce) {
		fmt.Fprintln(stderr, "error", ce.Error())
		switch ce.Stage {
		case core.StageParse:
			return exitDamaged
		default:
			return exitInput
		}
	}
	fmt.Fprintln(stderr, "error:", err)
	return exitInput
}
