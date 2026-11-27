package mediacore

import (
	"bytes"
	"io"
	"sort"
)

func stringReader(s string) io.Reader { return bytes.NewReader([]byte(s)) }

func byteReader(b []byte) io.Reader { return bytes.NewReader(b) }

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedByteKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
