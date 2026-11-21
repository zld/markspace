// Package integration wires the two WebRTC hops, the forwarder and the
// metrics collectors into a single-process loopback run.
package integration

import (
	"context"
	"fmt"
	"time"

	"looprelay/internal/report"
	"looprelay/internal/signal"
)

// Run executes the full loopback pipeline. Peer connections, the signaling
// listener and all worker goroutines are closed before it returns.
func Run(ctx context.Context, opts Options) Result {
	started := time.Now()

	srv, err := signal.NewServer(opts.Listen, opts.Timeout)
	if err != nil {
		return failure(ExitSignaling, fmt.Errorf("signaling listen: %w", err))
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()

	client := signal.NewClient(srv.BaseURL(), opts.Timeout)
	opened, err := client.Open(ctx)
	if err != nil {
		_ = shutdownServer(srv)
		<-serveErr
		return failure(ExitSignaling, fmt.Errorf("open signaling round: %w", err))
	}

	run, runErr := runRound(ctx, opts, client, opened.RequestID, started)
	run.Report.RequestID = opened.RequestID
	run.Report.Listen = srv.Addr().String()

	if runErr != nil {
		run.Report.Status = report.StatusFailed
		run.Report.Error = runErr.Error()
		run.Exit = classifyExit(runErr)
	}
	if err := client.Finish(context.Background(), opened.RequestID); err != nil && runErr == nil {
		run.Report.Status = report.StatusFailed
		run.Report.Error = fmt.Sprintf("finish signaling round: %v", err)
		run.Exit = ExitSignaling
	}
	if err := shutdownServer(srv); err != nil && runErr == nil {
		run.Report.Status = report.StatusFailed
		run.Report.Error = fmt.Sprintf("shutdown signaling: %v", err)
		run.Exit = ExitSignaling
	}
	if err := <-serveErr; err != nil && runErr == nil {
		run.Report.Status = report.StatusFailed
		run.Report.Error = fmt.Sprintf("serve signaling: %v", err)
		run.Exit = ExitSignaling
	}
	if runErr == nil {
		run.Report.Status = report.StatusOK
		for _, c := range run.Report.Checks {
			if !c.OK {
				run.Report.Status = report.StatusFailed
				if run.Exit == ExitOK {
					run.Exit = ExitIntegrity
				}
				break
			}
		}
	}
	run.Report.DurationMS = time.Since(started).Milliseconds()
	return run
}
