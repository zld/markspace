// Command regress runs the local RTP replay pipeline end to end against a
// generated fixture directory and prints the quantitative JSON summary.
//
// Exit codes:
//
//	0 success (recoverable damage is reported in the summary)
//	1 invalid arguments
//	2 input is damaged beyond the recovery window or cannot be parsed
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"rtpreplay/audio"
	"rtpreplay/fingerprint"
	"rtpreplay/ingest"
	"rtpreplay/media"
	"rtpreplay/pipeline"
	"rtpreplay/report"
	"rtpreplay/segment"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type options struct {
	input    string
	bitrates []int
	recover  bool
	jsonOut  bool
	outPath  string
}

func parseFlags(args []string) (options, *media.LocatableError) {
	fs := flag.NewFlagSet("regress", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var bitratesCSV string
	var opts options
	fs.StringVar(&opts.input, "input", "", "fixture directory (required)")
	fs.StringVar(&bitratesCSV, "bitrates", "16,32,64", "comma-separated bitrate tiers in kbps")
	fs.BoolVar(&opts.recover, "recover", false, "apply recovery and compare against reference")
	fs.BoolVar(&opts.jsonOut, "json", false, "emit JSON (always JSON today; flag retained for contract)")
	fs.StringVar(&opts.outPath, "out", "", "optional report path (must be outside the input directory)")
	if err := fs.Parse(args); err != nil {
		return opts, media.NewError(media.KindBadInput, "BAD_FLAGS", "regress", -1, "%v", err)
	}
	if opts.input == "" {
		return opts, media.NewError(media.KindBadInput, "MISSING_INPUT", "regress", -1, "--input is required")
	}
	parsed, err := parseBitrates(bitratesCSV)
	if err != nil {
		return opts, err
	}
	opts.bitrates = parsed
	return opts, nil
}

func run(args []string) int {
	opts, flagErr := parseFlags(args)
	if flagErr != nil {
		fmt.Fprintln(os.Stderr, flagErr.Error())
		return 1
	}

	summary, runErr := execute(opts)
	if summary != nil {
		emit(summary, opts)
	}
	if runErr != nil {
		if le, ok := media.AsLocatable(runErr); ok {
			fmt.Fprintln(os.Stderr, le.Error())
		} else {
			fmt.Fprintln(os.Stderr, runErr.Error())
		}
		// Any terminal pipeline/parse/recovery failure is unrecoverable input.
		return 2
	}
	return 0
}

// execute runs the full pipeline and returns a best-effort summary plus any
// terminal error.
func execute(opts options) (*report.Summary, error) {
	var audioStreams, videoStreams []*ingest.Stream
	var parseErrors []*media.LocatableError

	for _, kbps := range opts.bitrates {
		aPath := filepath.Join(fixtureAudioDir(opts.input), itoa(kbps)+".rtplog")
		ast, err := ingest.ParseRTPFile(aPath, media.AudioClockRate, kbps)
		if err != nil {
			parseErrors = append(parseErrors, mustLoc(err))
		} else {
			audioStreams = append(audioStreams, ast)
		}
		vPath := filepath.Join(fixtureVideoDir(opts.input), itoa(kbps)+".rtplog")
		vst, err := ingest.ParseRTPFile(vPath, media.VideoClockRate, kbps)
		if err != nil {
			parseErrors = append(parseErrors, mustLoc(err))
		} else {
			videoStreams = append(videoStreams, vst)
		}
	}
	if len(audioStreams) == 0 && len(videoStreams) == 0 {
		le := firstOr(parseErrors, "NO_PARSEABLE_STREAMS")
		return nil, le
	}

	audioReps, err := pipeline.Fanout(audioStreams, pipeline.KindAudio, pipeline.DefaultMaxGap)
	if err != nil {
		return nil, err
	}
	videoReps, err := pipeline.Fanout(videoStreams, pipeline.KindVideo, pipeline.DefaultMaxGap)
	if err != nil {
		return nil, err
	}
	for _, r := range audioReps {
		r.LocateBoundaries(200) // 200 ms segment period
	}
	for _, r := range videoReps {
		r.LocateBoundaries(500) // 500 ms segment period
	}

	audioSync := pipeline.FanIn(audioReps)
	videoSync := pipeline.FanIn(videoReps)

	var segs []segment.Segment
	for _, r := range append(append([]*pipeline.Representation{}, audioReps...), videoReps...) {
		segs = append(segs, segment.Split(r)...)
	}

	var silence []audio.SilenceRegion
	if len(audioReps) > 0 {
		silence = audio.DetectSilence(audioReps[0], audio.DefaultLevelThreshold)
	}
	var dups []fingerprint.Duplicate
	if len(audioReps) > 0 {
		dups = fingerprint.Dedupe(audioReps[0], fingerprint.DefaultWindow)
	}

	allReps := append(append([]*pipeline.Representation{}, audioReps...), videoReps...)
	// Surface the worst family-level sync error in the headline metric.
	headline := audioSync
	if videoSync.SyncErrorMS > headline.SyncErrorMS {
		headline = videoSync
	}
	summary := report.Build(allReps, headline, segs, silence, dups)
	for _, le := range parseErrors {
		summary.AttachError(asError(le))
	}

	// Container metadata evidence: failures are attached, not fatal, when the
	// RTP streams themselves parsed.
	attachContainerErrors(opts.input, summary)

	// A hole beyond the repair window is intrinsically unrecoverable,
	// independent of whether a reference comparison is requested.
	if le := checkUnrecoverable(allReps); le != nil {
		summary.AttachError(le)
		return summary, le
	}
	if opts.recover {
		compareReference(opts, summary)
	}
	return summary, nil
}

func emit(summary *report.Summary, opts options) {
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Println(string(data))
	if opts.outPath != "" {
		if werr := summary.WriteJSON(opts.outPath, opts.input); werr != nil {
			fmt.Fprintln(os.Stderr, werr.Error())
		}
	}
}

func mustLoc(err error) *media.LocatableError {
	if le, ok := media.AsLocatable(err); ok {
		return le
	}
	return media.NewError(media.KindBadInput, "PARSE_FAILED", "regress", -1, "%v", err)
}

func asError(le *media.LocatableError) error { return le }

func firstOr(errs []*media.LocatableError, code string) *media.LocatableError {
	if len(errs) > 0 {
		return errs[0]
	}
	return media.NewError(media.KindUnrecoverable, code, "regress", -1, "no streams parsed")
}

func checkUnrecoverable(reps []*pipeline.Representation) *media.LocatableError {
	for _, r := range reps {
		if len(r.Unrecoverable) > 0 {
			// The first unrecoverable extended sequence number is the
			// locator: it pinpoints where the repair window was exceeded.
			return media.NewError(media.KindUnrecoverable, "UNRECOVERABLE_LOSS", "pipeline",
				int64(r.Unrecoverable[0]),
				"%s@%dkbps: %d sequence numbers beyond the repair window starting at ext_seq %d",
				r.Kind, r.BitrateKbps, len(r.Unrecoverable), r.Unrecoverable[0])
		}
	}
	return nil
}

func fixtureAudioDir(root string) string { return filepath.Join(root, "audio") }
func fixtureVideoDir(root string) string { return filepath.Join(root, "video") }

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}
