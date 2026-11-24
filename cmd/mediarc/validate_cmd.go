package main

import (
	"flag"
	"fmt"
	"io"

	"mediarc/internal/ingest"
	"mediarc/internal/model"
	"mediarc/internal/pipeline"
)

func runValidate(argv []string, stdout io.Writer) (int, error) {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	workdir := fs.String("workdir", "", "demo working directory")
	if err := fs.Parse(argv); err != nil {
		return 1, err
	}
	if *workdir == "" {
		return 1, fmt.Errorf("validate requires --workdir")
	}
	p := pipeline.NewPaths(*workdir)
	faults := &model.Collector{}
	frames, err := ingest.ScanFrameFile(p.Frames, faults)
	if err != nil {
		return 1, err
	}
	packets, err := ingest.ReadRTPFile(p.Audio, faults)
	if err != nil {
		return 1, err
	}
	corrupt := faults.Count(model.FaultBadSHA, model.FaultTruncated, model.FaultBadLength, model.FaultBadTimestamp, model.FaultBadMagic)
	fmt.Fprintf(stdout, "frame_records=%d\n", len(frames))
	fmt.Fprintf(stdout, "rtp_records=%d\n", len(packets))
	fmt.Fprintf(stdout, "corrupt_inputs=%d\n", corrupt)
	for _, f := range faults.All() {
		if f.Kind == model.FaultBadSHA || f.Kind == model.FaultTruncated {
			fmt.Fprintf(stdout, "fault=%s material=%s record=%d offset=%d\n", f.Kind, f.MaterialID, f.RecordIndex, f.Offset)
		}
	}
	if corrupt > 0 {
		return model.ExitCorruptInput, model.NewExitError(model.ExitCorruptInput, fmt.Sprintf("%d corrupt input records", corrupt))
	}
	return 0, nil
}
