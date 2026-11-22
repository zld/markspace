// Package cli implements the tsrebuild subcommands. Argument mistakes are
// returned as UsageError (process exit code 2); unreadable or unparseable
// input is a plain error (exit code 1); success exits 0.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"tsrebuild/internal/fixture"
	"tsrebuild/internal/pipeline"
	"tsrebuild/internal/report"
	"tsrebuild/internal/segmenter"
)

// UsageError marks a command-line usage mistake that maps to exit code 2.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return "usage: " + e.Msg }

// AsUsageError reports whether err is a UsageError.
func AsUsageError(err error) bool {
	var u *UsageError
	return errors.As(err, &u)
}

func usage(format string, args ...any) error {
	return &UsageError{Msg: fmt.Sprintf(format, args...)}
}

// Input opens the named file, or returns stdin when name is "-" or empty.
func Input(name string) (io.ReadCloser, error) {
	if name == "" || name == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("opening input %q: %w", name, err)
	}
	return f, nil
}

// Generate runs the `generate` subcommand.
func Generate(outPath, scenario string) error {
	if outPath == "" {
		return usage("generate requires --out")
	}
	if scenario == "" {
		scenario = fixture.ScenarioRemap
	}
	valid := map[string]bool{
		fixture.ScenarioBasic: true, fixture.ScenarioRemap: true,
		fixture.ScenarioBadCRC: true, fixture.ScenarioFragment: true,
	}
	if !valid[scenario] {
		return usage("unknown scenario %q", scenario)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("creating %q: %w", outPath, err)
	}
	defer f.Close()
	manifest, err := fixture.Generate(f, fixture.Options{Scenario: scenario})
	if err != nil {
		return fmt.Errorf("generating scenario: %w", err)
	}
	fmt.Printf("wrote %d packets to %s (scenario=%s)\n", manifest.TotalPackets, outPath, scenario)
	return nil
}

// Inspect runs the `inspect` subcommand and writes the report to reportOut.
func Inspect(inputPath string, programNumber int, asJSON bool, reportOut io.Writer) error {
	if programNumber <= 0 || programNumber > 65535 {
		return usage("--program must be in 1..65535, got %d", programNumber)
	}
	in, err := Input(inputPath)
	if err != nil {
		return err
	}
	defer in.Close()

	eng := pipeline.New(in, uint16(programNumber))
	for {
		_, err := eng.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parsing input: %w", err)
		}
	}
	if eng.Timeline() == nil || len(eng.Timeline().Programs()) == 0 {
		return fmt.Errorf("no valid PAT/PMT mapping found for program %d", programNumber)
	}
	patRej, pmtRej := eng.RejectedCRC()
	doc := report.BuildInspect(eng.Timeline(), eng.Stats(), eng.PacketCount(), patRej, pmtRej)

	if asJSON {
		return report.WriteJSON(reportOut, doc)
	}
	writeInspectSummary(reportOut, doc)
	return nil
}

// Segment runs the `segment` subcommand, writing selected TS packets to tsOut
// and the accounting report (JSON when asJSON) to reportOut.
func Segment(inputPath string, programNumber int, tsPath string, asJSON bool, reportOut io.Writer) error {
	if programNumber <= 0 || programNumber > 65535 {
		return usage("--program must be in 1..65535, got %d", programNumber)
	}
	if tsPath == "" {
		return usage("segment requires --out for the output TS file")
	}
	in, err := Input(inputPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(tsPath)
	if err != nil {
		return fmt.Errorf("creating %q: %w", tsPath, err)
	}
	rep, err := segmenter.Run(in, out, uint16(programNumber))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("segmenting input: %w", err)
	}

	doc := report.BuildSegment(rep)
	if asJSON {
		if jerr := report.WriteJSON(reportOut, doc); jerr != nil {
			return jerr
		}
	} else {
		writeSegmentSummary(reportOut, doc)
	}
	return nil
}

func writeInspectSummary(w io.Writer, d report.StreamReport) {
	fmt.Fprintf(w, "transport_stream_id=%d program=%d total_packets=%d\n",
		d.TransportStreamID, d.ProgramNumber, d.TotalPackets)
	fmt.Fprintf(w, "pat_version_changes=%d pmt_version_changes=%d rejected_crc(pat=%d,pmt=%d)\n",
		d.PATVersionChanges, d.PMTVersionChanges, d.RejectedCRC.PAT, d.RejectedCRC.PMT)
	for i, iv := range d.MappingIntervals {
		fmt.Fprintf(w, "interval[%d] packets [%d,%d): pat_v=%d pmt_v=%d pmt_pid=%d pcr_pid=%d\n",
			i, iv.StartPacket, iv.EndPacket, iv.PATVersion, iv.PMTVersion, iv.PMTPID, iv.PCRPID)
		for _, s := range iv.Streams {
			fmt.Fprintf(w, "  %-7s pid=%d stream_type=0x%02x\n", s.Kind, s.PID, s.StreamType)
		}
	}
	fmt.Fprintln(w, "per-pid counts (per interval):")
	for _, s := range d.PIDStats {
		fmt.Fprintf(w, "  pid=%-4d kind=%-7s packets=%-4d gaps=%d duplicates=%d transport_errors=%d range=[%d,%d)\n",
			s.PID, s.Kind, s.Packets, s.ContinuityGaps, s.DuplicateCounters, s.TransportErrors,
			s.IntervalStart, s.IntervalEnd)
	}
}

func writeSegmentSummary(w io.Writer, d report.SegmentReport) {
	fmt.Fprintf(w, "program=%d input_packets=%d written_packets=%d pat_packets=%d\n",
		d.ProgramNumber, d.InputPackets, d.WrittenPackets, d.PATPackets)
	for i, iv := range d.Intervals {
		fmt.Fprintf(w, "interval[%d] packets [%d,%d): pat_v=%d pmt_v=%d\n",
			i, iv.StartPacket, iv.EndPacket, iv.PATVersion, iv.PMTVersion)
		for _, c := range iv.Written {
			fmt.Fprintf(w, "  pid=%-4d kind=%-7s written=%d\n", c.PID, c.Kind, c.Packets)
		}
	}
}
