package receiver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"jitterlab/internal/buffer"
	"jitterlab/internal/metrics"
	"jitterlab/internal/netem"
	"jitterlab/internal/packet"
	"jitterlab/internal/testkit"
)

// LiveConfig controls a real-time loopback run.
type LiveConfig struct {
	Buffer        buffer.Config
	Decay         float64
	MaxInvalidRun int
	// Duration bounds the run; zero means the run ends when the plan is
	// exhausted and the controller has drained. Timeout is the socket read
	// deadline.
	Duration time.Duration
	Timeout  time.Duration

	Options testkit.Options
}

// LiveResult is the outcome of a real-time run.
type LiveResult struct {
	Summary    metrics.Summary
	Frames     []metrics.Frame
	Port       int
	Sent       int
	Lost       int
	Faults     int
	Duplicated int
	ExitCode   int
	Err        error
}

// Live runs a loopback sender and receiver against a generated plan.
//
// The receiver socket is created first so the sender knows where to aim.
// One goroutine reads and feeds the shared controller; the caller's
// goroutine owns the playout clock. The sender, reader, both sockets and
// the timer are released before Live returns, on every path.
func Live(ctx context.Context, cfg LiveConfig) (LiveResult, error) {
	if err := cfg.Options.Validate(); err != nil {
		return LiveResult{ExitCode: ExitUsage}, err
	}
	if cfg.MaxInvalidRun <= 0 {
		cfg.MaxInvalidRun = DefaultMaxInvalidRun
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 200 * time.Millisecond
	}
	ctl, err := buffer.New(cfg.Buffer, cfg.Decay)
	if err != nil {
		return LiveResult{ExitCode: ExitUsage}, err
	}
	guard, err := NewGuard(cfg.MaxInvalidRun)
	if err != nil {
		return LiveResult{ExitCode: ExitUsage}, err
	}
	plan, err := testkit.Generate(cfg.Options)
	if err != nil {
		return LiveResult{ExitCode: ExitUsage}, err
	}

	rx, err := netem.Listen(0)
	if err != nil {
		return LiveResult{ExitCode: ExitOutput}, err
	}
	tx, err := netem.Dial(rx.Port())
	if err != nil {
		rx.Close()
		return LiveResult{ExitCode: ExitOutput}, err
	}

	var closeOnce sync.Once
	closeSockets := func() {
		closeOnce.Do(func() {
			rx.Close()
			tx.Close()
		})
	}
	defer closeSockets()

	res := LiveResult{Port: rx.Port(), ExitCode: ExitOK}
	rec := metrics.New(cfg.Buffer, cfg.Options.Seed)
	start := time.Now()

	var (
		stopOnce sync.Once
		stop     = make(chan struct{})
	)
	stopSender := func() { stopOnce.Do(func() { close(stop) }) }

	var (
		mu        sync.Mutex
		readErr   error
		sendStats netem.SendStats
		sendErr   error
	)
	// The recorder is touched by the reader (datagrams) and the playout
	// loop (slots), so every access goes through recMu.
	var recMu sync.Mutex
	observeFrame := func(slot buffer.Slot) metrics.Frame {
		recMu.Lock()
		defer recMu.Unlock()
		return rec.ObserveSlot(slot)
	}

	senderDone := make(chan struct{})
	go func() {
		defer close(senderDone)
		st, err := netem.PlayPlan(tx, plan, stop)
		mu.Lock()
		sendStats, sendErr = st, err
		mu.Unlock()
	}()

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, packet.MaxPacketSize+64)
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			default:
			}
			dg, rerr := rx.Read(buf, cfg.Timeout)
			if rerr != nil {
				if stopped(stop) || ctx.Err() != nil || errors.Is(rerr, net.ErrClosed) {
					return
				}
				var ne net.Error
				if errors.As(rerr, &ne) && ne.Timeout() {
					// An idle socket is fine while the sender is pacing;
					// after the plan is exhausted, keep waiting briefly for
					// stragglers and then stop.
					select {
					case <-senderDone:
						return
					default:
						continue
					}
				}
				mu.Lock()
				readErr = fmt.Errorf("receiver: read: %w", rerr)
				mu.Unlock()
				return
			}
			at := time.Since(start).Milliseconds()
			f, decErr := packet.Decode(dg.Data)
			recMu.Lock()
			rec.ObserveDatagram(len(dg.Data), decErr)
			recMu.Unlock()
			if gerr := guard.Observe(decErr); gerr != nil {
				mu.Lock()
				readErr = gerr
				mu.Unlock()
				stopSender()
				return
			}
			if decErr == nil {
				ctl.Offer(f, at)
			}
		}
	}()

	frameMs := int64(cfg.Buffer.FrameMs)
	maxMs := int64(cfg.Options.Frames)*frameMs +
		int64(cfg.Buffer.MaxWaitMs) + 4*int64(cfg.Timeout/time.Millisecond)
	playout := func() bool {
		slot := ctl.Tick(time.Since(start).Milliseconds())
		if slot.Played {
			res.Frames = append(res.Frames, observeFrame(slot))
		}
		return slot.Finished
	}

	for {
		if ctx.Err() != nil {
			break
		}
		mu.Lock()
		bad := readErr
		mu.Unlock()
		if bad != nil {
			res.Err = bad
			res.ExitCode = ExitDamaged
			break
		}
		// Only mark the input complete once the reader has stopped; it
		// keeps the socket open until the sender is done and a read times
		// out with nothing left to deliver.
		if stopped(readerDone) {
			ctl.Finish()
		}
		elapsed := time.Since(start).Milliseconds()
		if playout() {
			break
		}
		if elapsed > maxMs || (cfg.Duration > 0 && elapsed > cfg.Duration.Milliseconds()) {
			break
		}
		next := (elapsed/frameMs + 1) * frameMs
		timer := time.NewTimer(time.Until(start.Add(time.Duration(next) * time.Millisecond)))
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
	}

	// Let the sender exit on the stop signal before closing the sockets,
	// so its final write cannot fail and be mistaken for an I/O error.
	// Closing the receiver then unblocks the reader's pending Read.
	stopSender()
	<-senderDone
	closeSockets()
	<-readerDone

	mu.Lock()
	res.Sent, res.Lost, res.Faults = sendStats.Sent, sendStats.Lost, sendStats.Faults
	res.Duplicated = sendStats.Duplicated
	if sendErr != nil {
		res.Err = sendErr
		res.ExitCode = ExitOutput
	}
	mu.Unlock()

	// Drain: the sender has stopped, so finish the controller and play out
	// whatever it still holds.
	ctl.Finish()
	for i := 0; i < 1+cfg.Buffer.MaxWaitMs/cfg.Buffer.FrameMs+2; i++ {
		slot := ctl.Tick(time.Since(start).Milliseconds() + int64(i+1)*frameMs)
		if slot.Played {
			res.Frames = append(res.Frames, observeFrame(slot))
		}
		if slot.Finished {
			break
		}
	}

	recMu.Lock()
	rec.SetSenderStats(sendStats.Lost, countDuplicates(plan), plan.Stats.Reordered, sendStats.Faults)
	res.Summary = rec.Summary(ctl.Snapshot())
	recMu.Unlock()
	res.Summary.Seed = cfg.Options.Seed
	res.Summary.Frames = len(res.Frames)
	res.Summary.TargetMs = ctl.TargetMs()
	return res, res.Err
}

func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func countDuplicates(plan testkit.Plan) int {
	n := 0
	for _, ev := range plan.Events {
		if ev.Duplicate {
			n++
		}
	}
	return n
}
