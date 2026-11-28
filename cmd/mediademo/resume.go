package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mediakernel/internal/clock"
	"mediakernel/internal/dag"
	"mediakernel/internal/index"
	"mediakernel/internal/mp4scan"
	rangeread "mediakernel/internal/range"
	"mediakernel/internal/spool"
	"mediakernel/internal/verify"
)

// buildPipeline wires the five-node processing DAG rooted at dir.
func buildPipeline(dir string, trace []byte) (*dag.Scheduler, error) {
	s, err := dag.NewScheduler(dir)
	if err != nil {
		return nil, err
	}
	out := func(id string) string { return filepath.Join(dir, "out", id+".bin") }
	traceLen := uint64(len(trace))

	s.Add(&dag.Node{ID: "ingest", Config: "v1", Ranges: [][2]uint64{{0, traceLen}},
		Run: func() ([]byte, error) {
			data, st, err := reassembleTrace(trace)
			if err != nil {
				return nil, err
			}
			jdir := filepath.Join(dir, "work")
			if err := os.MkdirAll(jdir, 0o755); err != nil {
				return nil, err
			}
			j, err := spool.Open(filepath.Join(jdir, "journal.jsonl"))
			if err != nil {
				return nil, err
			}
			defer j.Close()
			if err := j.Append(spool.Entry{
				Offset: 0, Length: uint64(len(data)), SHA256: verify.Sum256Bytes(data),
			}); err != nil {
				return nil, err
			}
			_ = st
			return data, nil
		}})

	s.Add(&dag.Node{ID: "scan", Deps: []string{"ingest"}, Config: "v1",
		Ranges: [][2]uint64{{0, traceLen}},
		Run: func() ([]byte, error) {
			data, err := os.ReadFile(out("ingest"))
			if err != nil {
				return nil, err
			}
			c, err := mp4scan.Scan(bytes.NewReader(data))
			if err != nil {
				return nil, err
			}
			type tr struct {
				ID, Timescale, Samples uint32
			}
			var summary []tr
			for _, t := range c.Tracks {
				summary = append(summary, tr{t.ID, t.Timescale, uint32(len(t.Samples))})
			}
			return json.Marshal(summary)
		}})

	s.Add(&dag.Node{ID: "index", Deps: []string{"scan"}, Config: "v1",
		Ranges: [][2]uint64{{0, traceLen}},
		Run: func() ([]byte, error) {
			data, err := os.ReadFile(out("ingest"))
			if err != nil {
				return nil, err
			}
			c, err := mp4scan.Scan(bytes.NewReader(data))
			if err != nil {
				return nil, err
			}
			ix, err := index.Build(c)
			if err != nil {
				return nil, err
			}
			var buf bytes.Buffer
			for _, e := range ix.Entries {
				fmt.Fprintf(&buf, "t%d s%d dts%d off%d len%d\n",
					e.TrackID, e.SampleIndex, e.DTS, e.Offset, e.Size)
			}
			return buf.Bytes(), nil
		}})

	s.Add(&dag.Node{ID: "rangeread", Deps: []string{"index"}, Config: "v1:0-3s",
		Ranges: [][2]uint64{{0, traceLen}},
		Run: func() ([]byte, error) {
			data, err := os.ReadFile(out("ingest"))
			if err != nil {
				return nil, err
			}
			c, err := mp4scan.Scan(bytes.NewReader(data))
			if err != nil {
				return nil, err
			}
			ix, err := index.Build(c)
			if err != nil {
				return nil, err
			}
			start, end := clock.Zero(), clock.Rational{Num: 3, Den: 1}
			var reqs []rangeread.Request
			for _, tid := range ix.Tracks() {
				reqs = append(reqs, rangeread.Request{TrackID: tid, Start: start, End: end})
			}
			hits, err := rangeread.Read(ix, reqs...)
			if err != nil {
				return nil, err
			}
			var blob []byte
			for _, hh := range hits {
				b, err := rangeread.ReadData(bytes.NewReader(data), hh)
				if err != nil {
					return nil, err
				}
				blob = append(blob, b...)
			}
			return blob, nil
		}})

	s.Add(&dag.Node{ID: "checksum", Deps: []string{"rangeread"}, Config: "v1",
		Ranges: [][2]uint64{{0, traceLen}},
		Run: func() ([]byte, error) {
			blob, err := os.ReadFile(out("rangeread"))
			if err != nil {
				return nil, err
			}
			return []byte(verify.Sum256Bytes(blob)), nil
		}})
	return s, nil
}

// runPipeline executes the DAG once and returns the final checksum.
func runPipeline(dir string, trace []byte, interruptAfter int) (sha string, reused int, elapsed time.Duration, err error) {
	s, err := buildPipeline(dir, trace)
	if err != nil {
		return "", 0, 0, err
	}
	s.InterruptAfter = interruptAfter
	start := time.Now()
	err = s.Run()
	elapsed = time.Since(start)
	if err != nil {
		return "", s.Reused, elapsed, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "out", "checksum.bin"))
	if err != nil {
		return "", s.Reused, elapsed, err
	}
	return string(b), s.Reused, elapsed, nil
}

func cmdResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	dir := fs.String("dir", "", "work directory containing input.rtp")
	interrupt := fs.Int("interrupt-after", 0, "simulate a crash after N completed nodes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("resume: --dir is required")
	}
	trace, err := os.ReadFile(filepath.Join(*dir, "input.rtp"))
	if err != nil {
		return fmt.Errorf("resume: %v (run generate first)", err)
	}

	sha, reused, elapsed, err := runPipeline(*dir, trace, *interrupt)
	if err == dag.ErrInterrupted {
		fmt.Printf("interrupted after=%d checkpoint_dir=%s\n", *interrupt, *dir)
		return nil
	}
	if err != nil {
		return err
	}

	// Baseline: the same pipeline from scratch in a fresh directory.
	tmp, err := os.MkdirTemp("", "mediademo-full-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	fullSHA, _, fullElapsed, err := runPipeline(tmp, trace, 0)
	if err != nil {
		return err
	}
	if fullSHA != sha {
		return fmt.Errorf("resumed checksum %s differs from full run %s", sha, fullSHA)
	}
	fmt.Printf("resume_ms=%d full_ms=%d reused_nodes=%d sha256=%s\n",
		elapsed.Milliseconds(), fullElapsed.Milliseconds(), reused, sha)
	return nil
}
