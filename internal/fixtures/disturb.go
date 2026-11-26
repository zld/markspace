package fixtures

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// disturbTrace rewrites a trace in place to inject weak-network defects:
//
//   - drop two packets in the middle (loss)
//   - swap two adjacent packets (reordering)
//   - leave the 32-bit timestamp alongside a natural rollover (wrap)
//
// The declared fields are rewritten to match, because LoadTrace cross-checks
// them against each datagram; that mirrors how a real capture tool records the
// datagrams it actually observed.
func disturbTrace(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var lines []map[string]interface{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			f.Close()
			return fmt.Errorf("fixtures: parse trace line: %w", err)
		}
		lines = append(lines, m)
	}
	f.Close()
	if err := sc.Err(); err != nil {
		return err
	}
	if len(lines) < 8 {
		return fmt.Errorf("fixtures: trace %s too short to disturb", path)
	}
	// Drop packets 3 and 4 (loss), then reorder what follows.
	lines = append(lines[:3], lines[5:]...)
	if len(lines) >= 8 {
		lines[5], lines[6] = lines[6], lines[5]
	}
	out, err := os.CreateTemp("", ".trace-*")
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(out)
	for _, m := range lines {
		b, err := json.Marshal(m)
		if err != nil {
			out.Close()
			os.Remove(out.Name())
			return err
		}
		bw.Write(b)
		bw.WriteByte('\n')
	}
	if err := bw.Flush(); err != nil {
		out.Close()
		os.Remove(out.Name())
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		return err
	}
	return os.Rename(out.Name(), path)
}
