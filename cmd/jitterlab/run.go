package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"jitterlab/internal/ingest"
	"jitterlab/internal/jitter"
	"jitterlab/internal/plc"
	"jitterlab/internal/report"
	"jitterlab/internal/sim"
	"jitterlab/internal/wire"
)

// run executes the demo and returns the process exit code.
func run(o options) int {
	em := report.NewEmitter(os.Stdout)
	transports := []string{"udp", "tcp"}
	if o.transport != "both" {
		transports = []string{o.transport}
	}
	var wg sync.WaitGroup
	results := make(chan int, len(transports))
	for _, tr := range transports {
		wg.Add(1)
		go func(tr string) {
			defer wg.Done()
			results <- runPath(o, tr, em)
		}(tr)
	}
	wg.Wait()
	close(results)
	code := exitOK
	for c := range results {
		if c != exitOK {
			code = c
		}
	}
	return code
}

// runPath runs one transport end to end: simulate -> loopback receive
// -> shared controller -> playout clock -> report.
func runPath(o options, transport string, em *report.Emitter) int {
	events := sim.Generate(sim.Config{Seed: o.seed, Duration: o.duration, FramePeriod: o.frame})
	ctl := jitter.NewController(jitter.Config{
		MinTarget: 2, MaxTarget: 40, InitTarget: 3, FramePeriod: o.frame,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := ingest.Handler{
		OnPacket: func(p wire.Packet) {
			ctl.Add(jitter.Frame{Seq: p.Seq, Timestamp: p.Timestamp, PCM: p.Payload})
		},
		OnMalformed: ctl.NoteMalformed,
	}

	errCh := make(chan error, 2)
	var sendFn func([]byte) error
	switch transport {
	case "udp":
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			fmt.Fprintln(os.Stderr, "udp bind:", err)
			return exitLoopback
		}
		go func() { errCh <- ingest.ServeUDP(ctx, conn, handler) }()
		out, err := net.DialUDP("udp", nil, conn.LocalAddr().(*net.UDPAddr))
		if err != nil {
			fmt.Fprintln(os.Stderr, "udp dial:", err)
			return exitLoopback
		}
		defer out.Close()
		sendFn = func(b []byte) error {
			_ = out.SetWriteDeadline(time.Now().Add(time.Second))
			_, err := out.Write(b)
			return err
		}
	case "tcp":
		ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			fmt.Fprintln(os.Stderr, "tcp bind:", err)
			return exitLoopback
		}
		go func() { errCh <- ingest.ServeTCP(ctx, ln, handler) }()
		out, err := net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
		if err != nil {
			fmt.Fprintln(os.Stderr, "tcp dial:", err)
			return exitLoopback
		}
		defer out.Close()
		sendFn = func(b []byte) error {
			var hdr [2]byte
			binary.BigEndian.PutUint16(hdr[:], uint16(len(b)))
			_ = out.SetWriteDeadline(time.Now().Add(time.Second))
			if _, err := out.Write(hdr[:]); err != nil {
				return err
			}
			_, err := out.Write(b)
			return err
		}
	}

	// Sender: replay the schedule against the wall clock.
	go func() {
		start := time.Now()
		for _, ev := range events {
			if d := time.Until(start.Add(ev.At)); d > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(d):
				}
			}
			raw, err := wire.Encode(ev.Pkt)
			if err != nil {
				errCh <- err
				return
			}
			if err := sendFn(raw); err != nil && ctx.Err() == nil {
				errCh <- fmt.Errorf("send: %w", err)
				return
			}
		}
	}()

	// Playout clock + waterline sampler.
	conc := plc.NewConcealer(10)
	bt := plc.NewBoundaryTracker()
	ticks := int(o.duration / o.frame)
	tick := time.NewTicker(o.frame)
	curve := time.NewTicker(o.curve)
	start := time.Now()
	var prev []byte
	prevPLC := false
	done := false
	for !done {
		select {
		case err := <-errCh:
			if err != nil {
				fmt.Fprintln(os.Stderr, transport, "io:", err)
				tick.Stop()
				curve.Stop()
				return exitLoopback
			}
		case <-tick.C:
			f, ok := ctl.Tick()
			var out []byte
			plcInvolved := false
			if ok {
				conc.NoteGood(f.PCM)
				out = f.PCM
				plcInvolved = prevPLC
			} else {
				out = conc.Conceal()
				plcInvolved = true
			}
			if prev != nil {
				bt.Observe(prev, out, plcInvolved)
			}
			prev, prevPLC = out, !ok
			ticks--
			if ticks <= 0 {
				done = true
			}
		case <-curve.C:
			s := ctl.Snapshot()
			em.Waterline(report.Waterline{
				Transport:    transport,
				TMs:          time.Since(start).Milliseconds(),
				QueuedFrames: s.Queued,
				TargetFrames: s.Target,
			})
		}
	}
	tick.Stop()
	curve.Stop()
	// Let in-flight packets land, then shut the path down.
	time.Sleep(200 * time.Millisecond)
	cancel()
	st := ctl.Stats()
	em.Summary(report.Summary{
		Transport:       transport,
		FramesProcessed: st.Played + st.Concealed,
		PlayedFrames:    st.Played,
		ConcealedFrames: st.Concealed,
		ReceivedPackets: st.Received,
		DiscardedPackets: report.Discarded{
			Duplicate: st.Duplicate, Late: st.Late,
			Overflow: st.Overflow, Malformed: st.Malformed, Evicted: st.Evicted,
		},
		PLCContinuityPct:   bt.ContinuityPct(),
		PLCBoundaryJumpP95: bt.JumpP95(),
	})
	// Self-check: every received packet is accounted for.
	queued := uint64(ctl.Snapshot().Queued)
	accounted := st.Played + queued + st.Duplicate + st.Late + st.Overflow + st.Evicted
	if st.Received != accounted {
		fmt.Fprintf(os.Stderr, "%s: invariant: received %d != accounted %d\n", transport, st.Received, accounted)
		return exitInvariant
	}
	return exitOK
}
