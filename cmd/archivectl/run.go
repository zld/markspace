package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"archive/internal/index"
	"archive/internal/model"
	"archive/internal/recover"
	"archive/internal/rtp"
	"archive/internal/scan"
	"archive/internal/stream"
	"archive/internal/sync"
	"archive/internal/verify"
)

// runMeta carries the demo's ground truth so both demo and resume can report
// damage-localisation error against it.
type runMeta struct {
	CorruptAt          int64 `json:"corruptAt"`
	TruncateAt         int64 `json:"truncateAt"`
	CorruptPacketSize  int64 `json:"corruptPacketSize"`
	TruncatePacketSize int64 `json:"truncatePacketSize"`
}

func saveMeta(dir string, m *runMeta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644)
}

func loadMeta(dir string) *runMeta {
	var m runMeta
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil || json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &m
}

// runPipeline executes (or resumes) scan -> demux -> jitter -> sync -> verify
// for the working directory. It returns the process exit code.
func runPipeline(dir string, interruptAfter int) int {
	start := time.Now()
	inputPath := filepath.Join(dir, "input.mp4")
	digest, err := index.HashFile(inputPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: hash input:", err)
		return 1
	}
	st, err := os.Stat(inputPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	cp, cpErr := recover.Load(dir)
	switch {
	case cpErr == nil:
		if err := cp.VerifyInput(digest, st.Size()); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	case errors.Is(cpErr, os.ErrNotExist):
		cp = &recover.Checkpoint{InputSHA256: digest, InputSize: st.Size()}
	default:
		fmt.Fprintln(os.Stderr, "error:", cpErr)
		return 1
	}

	// Index: digest-bound cache; missing/incomplete/stale all fall back to
	// a fresh scan and a rebuild.
	idx, ierr := index.Load(dir, inputPath, digest)
	if ierr != nil {
		fmt.Println("index:", ierr)
		res, serr := scan.Scan(inputPath)
		if serr != nil {
			fmt.Fprintln(os.Stderr, "error: scan:", serr)
			return 1
		}
		idx = &index.Index{InputSHA256: digest, InputSize: st.Size(), Scan: res}
		if err := index.Save(dir, idx); err != nil {
			fmt.Fprintln(os.Stderr, "error: save index:", err)
			return 1
		}
		fmt.Println("index: rebuilt from scan")
	}
	res := idx.Scan

	if cp.Done {
		return verifyCompleted(dir, cp, res)
	}
	if cp.Committed > 0 {
		fmt.Printf("resume: %d packets already committed, continuing\n", cp.Committed)
	}

	f, err := os.Open(inputPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer f.Close()
	demux := stream.NewDemuxer(res, f, rtp.Config{Window: 8, ExpectedDelta: 3600})
	tracker := sync.NewTracker(sync.DefaultToleranceNs)

	hasher := verify.New()
	if cp.HashState != nil {
		hasher, err = verify.UnmarshalState(cp.HashState)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}
	outPath := filepath.Join(dir, "output.bin")
	out, err := os.OpenFile(outPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer out.Close()
	if cp.Committed == 0 {
		if err := out.Truncate(0); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	} else if _, err := out.Seek(0, io.SeekEnd); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	committed := cp.Committed
	i := 0
	for {
		pkt, err := demux.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: demux:", err)
			return 1
		}
		if i < committed { // already confirmed range: skip, deterministically
			i++
			continue
		}
		rec := encodeRecord(pkt)
		if _, err := out.Write(rec); err != nil {
			fmt.Fprintln(os.Stderr, "error: write output:", err)
			return 1
		}
		hasher.Write(rec)
		tracker.Observe(pkt.Type, pkt.PtsNs)
		i++
		// Checkpoint only at a complete packet boundary.
		cp.Committed = i
		cp.HashState, _ = hasher.MarshalState()
		cp.MaxDevNs = max64(cp.MaxDevNs, tracker.MaxDevNs)
		cp.AVViolations += tracker.Violations
		tracker.Violations = 0
		if err := recover.Save(dir, cp); err != nil {
			fmt.Fprintln(os.Stderr, "error: checkpoint:", err)
			return 1
		}
		if interruptAfter > 0 && i-committed >= interruptAfter {
			fmt.Printf("interrupted after %d packets; checkpoint saved (%d committed)\n",
				interruptAfter, i)
			fmt.Println("run: archivectl resume --dir", dir)
			return 2
		}
	}

	stats := demux.Stats()
	cp.Recovered, cp.Skipped, cp.Lost = stats.Recovered, stats.Skipped, stats.Lost
	cp.Done = true
	cp.DamagedInput = res.Errors.Damaged()
	cp.FinalSHA256 = hasher.SumHex()
	cp.Errs = append(res.Errors, demux.Errs...)
	if err := recover.Save(dir, cp); err != nil {
		fmt.Fprintln(os.Stderr, "error: checkpoint:", err)
		return 1
	}
	printReport(dir, res, cp, time.Since(start))
	if cp.DamagedInput {
		return 2
	}
	return 0
}

// verifyCompleted re-checks a finished run: the output must still hash to
// the checkpointed digest, which makes repeated resumes idempotent.
func verifyCompleted(dir string, cp *recover.Checkpoint, res *scan.Result) int {
	sum, err := index.HashFile(filepath.Join(dir, "output.bin"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: verify output:", err)
		return 1
	}
	if sum != cp.FinalSHA256 {
		fmt.Fprintf(os.Stderr, "error: %s: output %s != checkpoint %s\n",
			model.ErrVerify, sum[:12], cp.FinalSHA256[:12])
		return 1
	}
	fmt.Println("run already complete; output verified against checkpoint")
	printReport(dir, res, cp, 0)
	if cp.DamagedInput {
		return 2
	}
	return 0
}

// encodeRecord serialises one output packet deterministically.
func encodeRecord(p *stream.Packet) []byte {
	b := make([]byte, 14+len(p.Payload))
	copy(b[0:2], "AP")
	binary.BigEndian.PutUint16(b[2:4], uint16(p.TrackID))
	binary.BigEndian.PutUint64(b[4:12], uint64(p.PtsNs))
	binary.BigEndian.PutUint16(b[12:14], p.Seq)
	copy(b[14:], p.Payload)
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
