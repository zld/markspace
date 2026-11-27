// Command pack drives the multi-platform bitstream packing pipeline from the
// terminal. It has three subcommands:
//
//	pack demo    generate fixtures and run a (possibly crash-injected) job
//	pack resume  continue an interrupted job from its last verified stage
//	pack verify   validate a committed manifest and its fragments
//
// Exit codes: 0 success, 2 input/boundary failure with no partial commit,
// 3 manifest/verification failure, 1 any other error.
package main

import (
	"fmt"
	"os"
	"strings"

	"gamepack/internal/fixture"
	"gamepack/internal/stream"
	"gamepack/internal/txn"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "demo":
		err = runDemo(os.Args[2:])
	case "resume":
		err = runResume(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(1)
	}
	if err != nil {
		os.Exit(exitCode(err))
	}
}

type flags struct {
	root       string
	codecs     []stream.Codec
	bitrates   []int
	inject     map[fixture.Injection]bool
	crashAfter txn.Stage
}

func parseFlags(args []string) (*flags, error) {
	f := &flags{inject: map[fixture.Injection]bool{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		need := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s needs a value", arg)
			}
			i++
			return args[i], nil
		}
		switch arg {
		case "--root":
			v, err := need()
			if err != nil {
				return nil, err
			}
			f.root = v
		case "--codecs":
			v, err := need()
			if err != nil {
				return nil, err
			}
			for _, c := range strings.Split(v, ",") {
				codec := stream.Codec(strings.TrimSpace(c))
				if !codec.Valid() {
					return nil, fmt.Errorf("unsupported codec %q", c)
				}
				f.codecs = append(f.codecs, codec)
			}
		case "--bitrates":
			v, err := need()
			if err != nil {
				return nil, err
			}
			for _, b := range strings.Split(v, ",") {
				var n int
				if _, err := fmt.Sscanf(strings.TrimSpace(b), "%d", &n); err != nil || n <= 0 {
					return nil, fmt.Errorf("invalid bitrate %q", b)
				}
				f.bitrates = append(f.bitrates, n)
			}
		case "--inject":
			v, err := need()
			if err != nil {
				return nil, err
			}
			for _, inj := range strings.Split(v, ",") {
				switch fixture.Injection(strings.TrimSpace(inj)) {
				case fixture.InjectSwitch, fixture.InjectWrap, fixture.InjectTruncated:
					f.inject[fixture.Injection(strings.TrimSpace(inj))] = true
				default:
					return nil, fmt.Errorf("unknown injection %q", inj)
				}
			}
		case "--crash-after":
			v, err := need()
			if err != nil {
				return nil, err
			}
			f.crashAfter = txn.Stage(strings.TrimSpace(v))
		default:
			return nil, fmt.Errorf("unknown flag %q", arg)
		}
	}
	if f.root == "" {
		return nil, fmt.Errorf("--root is required")
	}
	if len(f.codecs) == 0 {
		f.codecs = []stream.Codec{stream.CodecH264}
	}
	if len(f.bitrates) == 0 {
		f.bitrates = []int{500}
	}
	return f, nil
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  pack demo   --root DIR [--codecs h264,h265,av1] [--bitrates 500,1000]
              [--inject switch,wrap,truncated] [--crash-after scan|align|mux|verify|commit]
  pack resume --root DIR
  pack verify --root DIR
`)
}
