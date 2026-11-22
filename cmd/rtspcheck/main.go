// Command rtspcheck validates RTSP/RTP session behavior between a
// client and a local in-process relay simulation over UDP and RTSP
// TCP-interleaved transports. It is fully local: the simulated server
// binds only to the loopback interface and serves a deterministic,
// code-generated RTP scenario.
//
// Exit codes:
//
//	0 - every session completed and matched the scenario expectations
//	1 - a session failed, or observed statistics did not match
//	2 - invalid command-line parameters or scenario configuration
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"rtspcheck/internal/report"
	"rtspcheck/internal/sim"
	"rtspcheck/internal/transport"
	"rtspcheck/testdata"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("rtspcheck", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	transportName := fs.String("transport", "both",
		"RTP transport: udp, interleaved or both")
	scenarioName := fs.String("scenario", testdata.ScenarioGenerated,
		"scenario: generated or wraparound")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	failMethod := fs.String("fail-method", "",
		"inject one RTSP method failure (DESCRIBE, SETUP, PLAY, TEARDOWN); testing hook")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected positional arguments: %v\n", fs.Args())
		return 2
	}
	if *failMethod != "" {
		if !validFailMethod(*failMethod) {
			fmt.Fprintf(os.Stderr, "invalid --fail-method %q\n", *failMethod)
			return 2
		}
	}

	modes, err := parseModes(*transportName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	scenario, err := testdata.New(*scenarioName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	builder := report.NewBuilder(report.Options{
		Scenario: scenario.Name,
		Expectation: report.Expectation{
			Transitions:      scenario.WantTransitions,
			SequenceGaps:     scenario.WantGaps,
			FramesComplete:   scenario.WantComplete,
			FramesIncomplete: scenario.WantIncomplete,
		},
	})

	sessionFailed := false
	for _, mode := range modes {
		result, err := runOne(mode, scenario, *failMethod)
		if err != nil {
			// Configuration-level problem: invalid for this invocation.
			fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
			return 2
		}
		builder.AddResult(string(mode), result)
		if result.Failure() != nil {
			sessionFailed = true
		}
	}

	rep := builder.Build()
	if *asJSON {
		raw, err := rep.Marshal()
		if err != nil {
			fmt.Fprintf(os.Stderr, "report error: %v\n", err)
			return 2
		}
		os.Stdout.Write(raw)
	} else {
		printText(rep)
	}

	if sessionFailed || !rep.ExpectationsMet {
		return 1
	}
	return 0
}

// parseModes resolves the --transport flag into one or two modes.
func parseModes(name string) ([]transport.Mode, error) {
	switch name {
	case "udp":
		return []transport.Mode{transport.ModeUDP}, nil
	case "interleaved", "tcp":
		return []transport.Mode{transport.ModeInterleaved}, nil
	case "both":
		return []transport.Mode{transport.ModeUDP, transport.ModeInterleaved}, nil
	default:
		return nil, fmt.Errorf(
			"invalid --transport %q (valid: udp, interleaved, both)", name)
	}
}

// validFailMethod restricts the failure-injection hook to the fixed
// set of RTSP method names the simulated server understands.
func validFailMethod(method string) bool {
	switch method {
	case "DESCRIBE", "SETUP", "PLAY", "TEARDOWN":
		return true
	}
	return false
}

// runOne starts a fresh loopback server, executes one session and
// always returns the observed result, even when the session failed.
func runOne(mode transport.Mode, scenario *testdata.Scenario,
	failMethod string,
) (*transport.Result, error) {
	srv, err := sim.New(sim.Config{
		Packets:        scenario.Packets,
		PacketInterval: 2 * time.Millisecond,
		FailMethod:     failMethod,
	})
	if err != nil {
		return nil, err
	}
	if err := srv.Start(); err != nil {
		return nil, err
	}
	defer srv.Close()

	common := transport.Config{
		ServerURL:          srv.URL(),
		ExpectedPackets:    len(scenario.Packets),
		RTPTimeout:         5 * time.Second,
		DrainQuietPeriod:   250 * time.Millisecond,
		ExpectedLastSeq:    scenario.ExpectedTailSeq,
		HasExpectedLastSeq: true,
		SSRC:               scenario.SSRC,
	}

	switch mode {
	case transport.ModeUDP:
		return transport.RunUDP(transport.UDPConfig{
			ServerURL:          common.ServerURL,
			ExpectedPackets:    common.ExpectedPackets,
			RTPTimeout:         common.RTPTimeout,
			DrainQuietPeriod:   common.DrainQuietPeriod,
			ExpectedLastSeq:    common.ExpectedLastSeq,
			HasExpectedLastSeq: common.HasExpectedLastSeq,
			SSRC:               common.SSRC,
		})
	default:
		return transport.RunInterleaved(transport.InterleavedConfig{
			ServerURL:          common.ServerURL,
			ExpectedPackets:    common.ExpectedPackets,
			RTPTimeout:         common.RTPTimeout,
			DrainQuietPeriod:   common.DrainQuietPeriod,
			ExpectedLastSeq:    common.ExpectedLastSeq,
			HasExpectedLastSeq: common.HasExpectedLastSeq,
			SSRC:               common.SSRC,
		})
	}
}

// printText renders a compact human-readable summary for terminals.
func printText(rep *report.Report) {
	fmt.Printf("scenario: %s  expectations_met: %v\n",
		rep.Scenario, rep.ExpectationsMet)
	for _, tr := range rep.Transports {
		fmt.Printf("- transport=%s ok=%v transitions=%d gaps=%d "+
			"frames_complete=%d frames_incomplete=%d packets=%d\n",
			tr.Transport, tr.OK, tr.StateTransitionN, tr.SequenceGapCount,
			tr.FramesComplete, tr.FramesIncomplete, tr.PacketsReceived)
		if tr.Failure != "" {
			fmt.Printf("    failure: %s (stage %s/%s)\n",
				tr.Failure, tr.FailureStage, tr.FailureMethod)
		}
	}
	for _, e := range rep.ExpectationErrs {
		fmt.Fprintf(os.Stderr, "expectation: %s\n", e)
	}
}
