package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"mediakernel/internal/clock"
	"mediakernel/internal/index"
	"mediakernel/internal/mp4scan"
	rangeread "mediakernel/internal/range"
	"mediakernel/internal/verify"
)

func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "MP4/MOV file to verify")
	tracePath := fs.String("trace", "", "RTP trace to reassemble and check against input")
	rangeStr := fs.String("range", "", "time range SN/SD:EN/ED (seconds)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("verify: --input is required")
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		return err
	}

	if *tracePath != "" {
		trace, err := os.ReadFile(*tracePath)
		if err != nil {
			return err
		}
		got, st, err := reassembleTrace(trace)
		if err != nil {
			return err
		}
		wantSum, gotSum := verify.Sum256Bytes(data), verify.Sum256Bytes(got)
		if wantSum != gotSum {
			return fmt.Errorf("reassembled payload differs from input: %s != %s", gotSum, wantSum)
		}
		fmt.Printf("reassembled bytes=%d dups=%d bandwidth_bps=%.0f wait_window_ns=%d transport_sha256=%s\n",
			st.Bytes, st.Dups, st.Bandwidth, st.WaitWindow, gotSum)
	}

	c, err := mp4scan.Scan(bytes.NewReader(data))
	if err != nil {
		return err
	}
	ix, err := index.Build(c)
	if err != nil {
		return err
	}

	samplesRead := 0
	sum := ""
	if *rangeStr != "" {
		sn, sd, en, ed, err := parseRange(*rangeStr)
		if err != nil {
			return err
		}
		start := clock.Rational{Num: sn, Den: sd}
		end := clock.Rational{Num: en, Den: ed}
		var reqs []rangeread.Request
		for _, tid := range ix.Tracks() {
			reqs = append(reqs, rangeread.Request{TrackID: tid, Start: start, End: end})
		}
		hits, err := rangeread.Read(ix, reqs...)
		if err != nil {
			return err
		}
		reader := bytes.NewReader(data)
		for _, hh := range hits {
			blob, err := rangeread.ReadData(reader, hh)
			if err != nil {
				return err
			}
			samplesRead += len(hh)
			sum = verify.Sum256Bytes(append([]byte(sum), blob...))
		}
	}

	fmt.Printf("tracks=%d index_entries=%d samples_read=%d sha256=%s\n",
		len(c.Tracks), ix.Len(), samplesRead, sum)
	return nil
}
