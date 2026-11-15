package cli

import (
	"flag"
	"fmt"
)

// DemoConfig drives the `demo` subcommand (synthetic CMAF generation).
type DemoConfig struct {
	Out       string
	Seed      int64
	Bitrates  []int
	Duration  int64 // milliseconds
	Part      int64 // milliseconds
	Faults    []string
	JSON      bool
}

// BuildConfig drives the `build` subcommand (manifest/segment assembly).
type BuildConfig struct {
	In       string
	Out      string
	FromSeq  int
	ToSeq    int
	Segment  int64 // milliseconds
	Part     int64 // milliseconds
	Window   int64 // milliseconds
	JSON     bool
}

// VerifyConfig drives the `verify` subcommand.
type VerifyConfig struct {
	In        string
	HLS       string
	Duration  int64 // milliseconds
	Tolerance int64 // milliseconds
	JSON      bool
}

func stringFlag(fs *flag.FlagSet, name, def, help string) *string {
	return fs.String(name, def, help)
}

// ParseDemo parses `demo` flags after the subcommand word.
func ParseDemo(args []string) (*DemoConfig, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	out := stringFlag(fs, "out", "", "output inbox directory")
	seed := fs.Int64("seed", 1, "deterministic generator seed")
	br := stringFlag(fs, "bitrates", "", "comma separated bitrates, e.g. 800000,1600000")
	dur := stringFlag(fs, "duration", "12s", "timeline duration, e.g. 12s")
	part := stringFlag(fs, "part", "500ms", "part duration, e.g. 500ms")
	faults := stringFlag(fs, "fault", "", "comma separated faults: missing,order,drift")
	js := fs.Bool("json", false, "emit JSON report")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	bitrates, err := ParseBitrates(*br)
	if err != nil {
		return nil, err
	}
	duration, err := ParseDuration(*dur)
	if err != nil {
		return nil, err
	}
	partMs, err := ParseDuration(*part)
	if err != nil {
		return nil, err
	}
	faultList, err := ParseFaults(*faults)
	if err != nil {
		return nil, err
	}
	if *out == "" {
		return nil, fmt.Errorf("--out is required")
	}
	if partMs <= 0 || duration <= 0 || duration%partMs != 0 {
		return nil, fmt.Errorf("duration must be a positive multiple of part duration")
	}
	return &DemoConfig{Out: *out, Seed: *seed, Bitrates: bitrates,
		Duration: duration, Part: partMs, Faults: faultList, JSON: *js}, nil
}

// ParseBuild parses `build` flags.
func ParseBuild(args []string) (*BuildConfig, error) {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	in := stringFlag(fs, "in", "", "CMAF inbox directory")
	out := stringFlag(fs, "out", "", "HLS output directory")
	from := fs.Int("from-seq", 1, "first expected fragment sequence number")
	to := fs.Int("to-seq", 0, "last expected fragment sequence number")
	seg := stringFlag(fs, "segment", "2s", "segment duration, e.g. 2s")
	part := stringFlag(fs, "part", "500ms", "part duration, e.g. 500ms")
	win := stringFlag(fs, "window", "12s", "rolling window length, e.g. 12s")
	js := fs.Bool("json", false, "emit JSON report")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	segment, err := ParseDuration(*seg)
	if err != nil {
		return nil, err
	}
	partMs, err := ParseDuration(*part)
	if err != nil {
		return nil, err
	}
	window, err := ParseDuration(*win)
	if err != nil {
		return nil, err
	}
	if *in == "" || *out == "" {
		return nil, fmt.Errorf("--in and --out are required")
	}
	if *to < *from {
		return nil, fmt.Errorf("--to-seq must be >= --from-seq")
	}
	if partMs <= 0 || segment%partMs != 0 || window < segment {
		return nil, fmt.Errorf("segment must be a multiple of part and window >= segment")
	}
	return &BuildConfig{In: *in, Out: *out, FromSeq: *from, ToSeq: *to,
		Segment: segment, Part: partMs, Window: window, JSON: *js}, nil
}

// ParseVerify parses `verify` flags.
func ParseVerify(args []string) (*VerifyConfig, error) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	in := stringFlag(fs, "in", "", "CMAF inbox directory")
	hls := stringFlag(fs, "hls", "", "HLS output directory")
	dur := stringFlag(fs, "duration", "12s", "expected timeline duration")
	tol := stringFlag(fs, "tolerance", "50ms", "cross-bitrate drift tolerance")
	js := fs.Bool("json", false, "emit JSON report")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	duration, err := ParseDuration(*dur)
	if err != nil {
		return nil, err
	}
	tolerance, err := ParseDuration(*tol)
	if err != nil {
		return nil, err
	}
	if *in == "" || *hls == "" {
		return nil, fmt.Errorf("--in and --hls are required")
	}
	return &VerifyConfig{In: *in, HLS: *hls, Duration: duration,
		Tolerance: tolerance, JSON: *js}, nil
}
