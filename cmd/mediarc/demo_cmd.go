package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"mediarc/internal/model"
	"mediarc/internal/pipeline"
)

func runDemo(argv []string, stdout io.Writer) (int, error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := pipeline.DefaultOptions()
	var inject string
	fs.Int64Var(&o.Seed, "seed", o.Seed, "deterministic fixture seed")
	fs.IntVar(&o.Frames, "frames", o.Frames, "logical video frames")
	fs.IntVar(&o.AudioPackets, "audio-packets", o.AudioPackets, "canonical RTP audio packets")
	fs.IntVar(&o.ChunkSize, "chunk", o.ChunkSize, "frames per chunk")
	fs.IntVar(&o.ChunkSize, "chunk-size", o.ChunkSize, "alias for --chunk")
	fs.StringVar(&inject, "inject", "", "comma list: bad-sha,truncated,late,duplicate")
	fs.StringVar(&o.Workdir, "workdir", "", "local working directory")
	var targetRate uint
	fs.UintVar(&targetRate, "target-rate", uint(o.TargetRate), "normalized audio sample rate")
	fs.StringVar(&o.SourceLayout, "source-layout", o.SourceLayout, "mono|stereo|5.1")
	fs.StringVar(&o.TargetLayout, "target-layout", o.TargetLayout, "mono|stereo|5.1")
	strictCorrupt := fs.Bool("strict-corrupt", false, "exit 2 when generated input contains corruption")
	if err := fs.Parse(argv); err != nil {
		return 1, err
	}
	if o.Workdir == "" {
		return 1, fmt.Errorf("demo requires --workdir")
	}
	o.TargetRate = uint32(targetRate)
	for _, token := range strings.Split(inject, ",") {
		token = strings.TrimSpace(token)
		if token != "" {
			o.Inject = append(o.Inject, token)
		}
	}
	if _, _, err := o.Layouts(); err != nil {
		return model.ExitCode(err), err
	}
	res, err := pipeline.GenerateDemo(o)
	if err != nil {
		return 1, err
	}
	code := model.ExitOK
	if *strictCorrupt && res.Faults.Has(model.FaultBadSHA, model.FaultTruncated, model.FaultBadLength, model.FaultBadTimestamp) {
		code = model.ExitCorruptInput
		res.Summary.ExitCode = code
	}
	res.Summary.Print(stdout)
	return code, nil
}
