package main

import (
	"os"
	"path/filepath"

	"packcheck/internal/recovery"
	"packcheck/internal/report"
	"packcheck/internal/segment"
	"packcheck/internal/stream"
)

func names(specs []layerSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.name
	}
	return out
}

func ssrcFor(name string) uint32 {
	switch name {
	case "low":
		return 1111
	case "mid":
		return 2222
	default:
		return 3333
	}
}

func packetBytes(pkts []segment.Packet) int64 {
	var n int64
	for _, p := range pkts {
		n += int64(len(p.Raw))
	}
	return n
}

func healthyOnly(sigs []segment.LayerSignature) []segment.LayerSignature {
	var out []segment.LayerSignature
	for _, s := range sigs {
		if s.Healthy {
			out = append(out, s)
		}
	}
	return out
}

func sigsWithRecovery(m *recovery.Manager, sigs []segment.LayerSignature) []segment.LayerSignature {
	out := make([]segment.LayerSignature, len(sigs))
	copy(out, sigs)
	for i := range out {
		st := m.States()
		for _, s := range st {
			if s.Representation == out[i].Representation {
				out[i].Healthy = s.Healthy
			}
		}
	}
	return out
}

func verdictOf(m *recovery.Manager, name string) recovery.Verdict {
	for _, st := range m.States() {
		if st.Representation == name {
			return st.After
		}
	}
	return recovery.VerdictFailed
}

func containsName(list []string, name string) bool {
	for _, x := range list {
		if x == name {
			return true
		}
	}
	return false
}

func maxPeak(f *stream.FanOut) int {
	peak := 0
	for _, l := range f.Layers() {
		if p := l.Peak(); p > peak {
			peak = p
		}
	}
	return peak
}

func writeArtifacts(dir string, res *report.Result) error {
	if err := os.MkdirAll(filepath.Clean(dir), 0o755); err != nil {
		return err
	}
	path := filepath.Join(filepath.Clean(dir), "report.txt")
	return os.WriteFile(path, []byte(res.Render()), 0o644)
}
