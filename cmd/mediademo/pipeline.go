package main

import (
	"bytes"
	"fmt"
	"io"

	"mediakernel/internal/rtpin"
)

// reassembleStats summarizes one trace ingestion.
type reassembleStats struct {
	Bytes      int
	Dups       int
	Bandwidth  float64
	WaitWindow int64
}

// reassembleTrace reads every record of a trace and returns the reassembled
// file. The first unrecovered gap is reported as ERR_MISSING_SEGMENT; no
// data is fabricated for missing ranges.
func reassembleTrace(trace []byte) ([]byte, reassembleStats, error) {
	r := rtpin.NewReassembler()
	tr := bytes.NewReader(trace)
	var out []byte
	off := int64(0)
	for {
		rec, err := rtpin.ReadRecord(tr, off)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, reassembleStats{}, err
		}
		off = int64(len(trace)) - int64(tr.Len())
		segs, err := r.Add(rec)
		if err != nil {
			return nil, reassembleStats{}, err
		}
		for _, s := range segs {
			out = append(out, s.Data...)
		}
	}
	if err := r.MissingError(); err != nil {
		return nil, reassembleStats{}, err
	}
	return out, reassembleStats{
		Bytes:      len(out),
		Dups:       r.Dups,
		Bandwidth:  r.BandwidthBps(),
		WaitWindow: r.WaitWindowNs(),
	}, nil
}

// parseRange parses "SN/SD:EN/ED" into numerator/denominator pairs.
func parseRange(s string) (sn, sd, en, ed int64, err error) {
	var c1, c2, c3 int
	err = fmt.Errorf("bad range %q, want SN/SD:EN/ED", s)
	n, _ := fmt.Sscanf(s, "%d/%d:%d/%d", &sn, &sd, &en, &ed)
	if n != 4 || sd == 0 || ed == 0 {
		return 0, 0, 0, 0, err
	}
	_, _, _ = c1, c2, c3
	return sn, sd, en, ed, nil
}
