package selftest

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pion/rtp"

	"mediacheck/internal/adapter"
	mp4a "mediacheck/internal/adapter/mp4"
	"mediacheck/internal/fault"
)

// RunSelftest generates all samples, runs the pipeline over them and
// prints the terminal summary. It returns the process exit code: 0 when
// every expectation holds, 1 otherwise. When keepDir is true the task
// directory is preserved and its path printed (for batch-mode checks).
func RunSelftest(cfg Config, w io.Writer, keepDir ...bool) int {
	keep := len(keepDir) > 0 && keepDir[0]
	ok := true
	check := func(cond bool, format string, args ...interface{}) {
		status := "PASS"
		if !cond {
			status = "FAIL"
			ok = false
		}
		fmt.Fprintf(w, "  check[%s]: %s\n", status, fmt.Sprintf(format, args...))
	}

	taskDir, err := os.MkdirTemp("", "media-check-")
	if err != nil {
		fmt.Fprintln(w, "selftest:", err)
		return 1
	}
	if keep {
		fmt.Fprintf(w, "task_dir=%s\n", taskDir)
	} else {
		defer os.RemoveAll(taskDir)
	}
	fixDir := filepath.Join(taskDir, "fixtures")
	outDir := filepath.Join(taskDir, "out")
	os.MkdirAll(fixDir, 0o755)
	os.MkdirAll(outDir, 0o755)

	// Deterministic fixtures.
	av := GenerateMedia(cfg.TargetKbps, true)
	normalPath := filepath.Join(fixDir, "normal.mp4")
	if err := WriteMP4(normalPath, av); err != nil {
		fmt.Fprintln(w, "selftest: generate normal.mp4:", err)
		return 1
	}
	vonly := GenerateMedia(cfg.TargetKbps, false)
	base := PacketizeVideo(vonly)
	reorderPath := filepath.Join(fixDir, "reorder.rtpcap")
	if err := WriteRTPCap(reorderPath, InjectReorderDup(base)); err != nil {
		fmt.Fprintln(w, "selftest:", err)
		return 1
	}
	drop := map[uint16]bool{}
	if s := FramePacketSeqs(base, 30); len(s) >= 2 {
		drop[s[len(s)/2]] = true
		drop[s[len(s)/2+1]] = true
	}
	if s := FramePacketSeqs(base, 49); len(s) > 0 {
		drop[s[len(s)-1]] = true // marker packet right before keyframe 50
	}
	lossPath := filepath.Join(fixDir, "loss.rtpcap")
	if err := WriteRTPCap(lossPath, InjectLoss(base, drop)); err != nil {
		fmt.Fprintln(w, "selftest:", err)
		return 1
	}
	nmPath := filepath.Join(fixDir, "nonmonotonic.rtpcap")
	if err := WriteRTPCap(nmPath, InjectNonMonotonic(base, packetIndexOfFrame(base, 60), 5*uint32(FrameDuration))); err != nil {
		fmt.Fprintln(w, "selftest:", err)
		return 1
	}
	truncPath := filepath.Join(fixDir, "truncated.mp4")
	if err := truncateFile(normalPath, truncPath, 2000); err != nil {
		fmt.Fprintln(w, "selftest:", err)
		return 1
	}
	fmt.Fprintln(w, "== fixtures ==")
	fmt.Fprintln(w, "generated: normal.mp4 reorder.rtpcap loss.rtpcap nonmonotonic.rtpcap truncated.mp4")

	reg := adapter.NewRegistry()
	if err := reg.Register(mp4a.ParserV1{}); err != nil {
		fmt.Fprintln(w, "selftest:", err)
		return 1
	}
	ctx := context.Background()

	fmt.Fprintln(w, "== sample normal (mp4) ==")
	res := ProcessMP4(ctx, cfg, reg, "normal", normalPath, outDir)
	PrintResult(w, res, nil)
	check(res.OK && !res.Skipped, "status=ok")
	check(res.Bitrate.Within(cfg.MaxBitrateDelta),
		"bitrate delta %.2f%% <= %.2f%%", res.Bitrate.Delta*100, cfg.MaxBitrateDelta*100)
	check(res.ManifestErr <= res.ManifestBound,
		"manifest boundary %dns <= %dns", res.ManifestErr, res.ManifestBound)

	fmt.Fprintln(w, "== sample reorder+dup (rtp) ==")
	out := &RTPOutcome{}
	res = ProcessRTP(ctx, cfg, "reorder", reorderPath, outDir, out)
	PrintResult(w, res, out)
	check(out.Stats.Reordered > 0, "reordered=%d > 0", out.Stats.Reordered)
	check(out.Stats.Duplicates > 0, "duplicates=%d > 0", out.Stats.Duplicates)
	check(out.Stats.Lost == 0, "lost=%d == 0", out.Stats.Lost)
	check(res.OK, "status=ok")

	fmt.Fprintln(w, "== sample loss-outside-window (rtp) ==")
	out = &RTPOutcome{}
	res = ProcessRTP(ctx, cfg, "loss", lossPath, outDir, out)
	PrintResult(w, res, out)
	check(out.Stats.Lost >= 3, "lost=%d >= 3", out.Stats.Lost)
	check(len(out.Ranges) >= 2, "recovery ranges=%d >= 2", len(out.Ranges))
	hasUnrec := false
	for _, r := range out.Ranges {
		if !r.Recoverable {
			hasUnrec = true
		}
	}
	check(hasUnrec, "unrecoverable range reported")
	check(res.OK, "status=ok (loss handled, nothing fabricated)")

	fmt.Fprintln(w, "== sample truncated (mp4) ==")
	res = ProcessMP4(ctx, cfg, reg, "truncated", truncPath, outDir)
	PrintResult(w, res, nil)
	check(res.Skipped, "file skipped, batch continues")
	check(res.Faults.Has(fault.ErrTruncated), "error code TRUNCATED_CONTAINER")
	check(faultOffset(res.Faults, fault.ErrTruncated) >= 0, "byte offset recorded")

	fmt.Fprintln(w, "== sample nonmonotonic (rtp) ==")
	out = &RTPOutcome{}
	res = ProcessRTP(ctx, cfg, "nonmonotonic", nmPath, outDir, out)
	PrintResult(w, res, out)
	check(res.Faults.Has(fault.ErrNonMonotonic), "error code NON_MONOTONIC_TS")
	check(faultOffset(res.Faults, fault.ErrNonMonotonic) >= 0, "byte offset recorded")

	fmt.Fprintln(w, "== sample slow-consumer ==")
	slowCfg := cfg
	slowCfg.ConsumerDelay = 2 * time.Millisecond
	res = ProcessMP4(ctx, slowCfg, reg, "slow", normalPath, outDir)
	check(res.OK, "status=ok")
	check(res.PeakBuffer <= cfg.MaxBufferBytes, "peak_buffer <= %d bytes", cfg.MaxBufferBytes)
	fmt.Fprintf(w, "  peak_buffer_within_limit=%v\n", res.PeakBuffer <= cfg.MaxBufferBytes)

	fmt.Fprintln(w, "== sample cancel ==")
	cancelCfg := cfg
	cancelCfg.ConsumerDelay = 50 * time.Millisecond
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan *Result, 1)
	go func() { done <- ProcessMP4(cctx, cancelCfg, reg, "cancel", normalPath, outDir) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case cres := <-done:
		check(!cres.OK, "task canceled")
		_, statErr := os.Stat(filepath.Join(outDir, "cancel.out.mp4"))
		check(os.IsNotExist(statErr), "no leftover temp output")
	case <-time.After(10 * time.Second):
		check(false, "goroutines exited after cancel")
	}

	fmt.Fprintln(w, "== summary ==")
	fmt.Fprintf(w, "config: jitter_ms=%d max_buffer_mb=%d target_kbps=%.0f max_bitrate_delta=%.2f\n",
		cfg.JitterMs, cfg.MaxBufferBytes>>20, cfg.TargetKbps, cfg.MaxBitrateDelta)
	if ok {
		fmt.Fprintln(w, "status=ok")
		return 0
	}
	fmt.Fprintln(w, "status=failed")
	return 1
}

// PrintResult writes the deterministic per-sample report.
func PrintResult(w io.Writer, res *Result, out *RTPOutcome) {
	switch {
	case res.Skipped:
		fmt.Fprintln(w, "status=skipped")
	case res.OK:
		fmt.Fprintln(w, "status=ok")
	default:
		fmt.Fprintln(w, "status=failed")
	}
	if out != nil {
		s := out.Stats
		fmt.Fprintf(w, "rtp: received=%d emitted=%d reordered=%d duplicates=%d late=%d lost=%d\n",
			s.Received, s.Emitted, s.Reordered, s.Duplicates, s.Late, s.Lost)
		for _, r := range out.Ranges {
			fmt.Fprintf(w, "recovery: seq=%d-%d media=[%dms,%dms] recoverable=%v reason=%q\n",
				r.FirstLostSeq, r.LastLostSeq, r.StartNanos/1e6, r.EndNanos/1e6, r.Recoverable, r.Reason)
		}
		for _, r := range out.Ranges {
			if !r.Recoverable {
				fmt.Fprintf(w, "unrecovered: [%dms,%dms]\n", r.StartNanos/1e6, r.EndNanos/1e6)
			}
		}
	}
	if res.Remuxed {
		fmt.Fprintf(w, "bitrate: %s\n", res.Bitrate.String())
	}
	if res.Manifest != nil && len(res.Manifest.Entries) > 0 {
		first := res.Manifest.Entries[0]
		last := res.Manifest.Entries[len(res.Manifest.Entries)-1]
		fmt.Fprintf(w, "manifest: entries=%d start=%dns end=%dns boundary_error=%dns bound=%dns\n",
			len(res.Manifest.Entries), first.StartNanos, last.EndNanos, res.ManifestErr, res.ManifestBound)
	}
	if res.Segments != nil {
		var maxDelta, cnt int64
		for _, s := range res.Segments {
			d := s.AVDeltaNanos
			if d < 0 {
				d = -d
			}
			if d > maxDelta {
				maxDelta = d
			}
			if s.Desync {
				cnt++
			}
		}
		fmt.Fprintf(w, "align: segments=%d max_abs_delta=%dns desync=%d\n", len(res.Segments), maxDelta, cnt)
	}
	if res.Faults.Len() == 0 {
		fmt.Fprintln(w, "faults: none")
	}
	for _, f := range res.Faults.All() {
		fmt.Fprintf(w, "fault: %s\n", f.Error())
	}
}

func faultOffset(fl *fault.List, code fault.Code) int64 {
	for _, f := range fl.All() {
		if f.Code == code {
			return f.Offset
		}
	}
	return -1
}

func packetIndexOfFrame(pkts []*rtp.Packet, frame uint32) int {
	for i, p := range pkts {
		if len(p.Payload) >= 5 && frameOf(p) == frame {
			return i
		}
	}
	return 0
}

func truncateFile(src, dst string, size int64) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if int64(len(raw)) > size {
		raw = raw[:size]
	}
	return os.WriteFile(dst, raw, 0o644)
}
