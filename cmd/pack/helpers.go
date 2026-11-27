package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"

	"gamepack/internal/fixture"
	"gamepack/internal/pipeline"
	"gamepack/internal/stream"
)

// hashRel hashes a fragment path relative to the job root.
func hashRel(root, rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// configFromRoot reconstructs a pipeline Config from the inputs directory so
// resume needs no re-supplied command-line parameters beyond --root.
func configFromRoot(root string) (pipeline.Config, error) {
	cfg := pipeline.Config{
		Root: root, Units: 12, KeyEvery: 4, WrapBits: 16,
		Inject: map[fixture.Injection]bool{},
	}
	entries, err := os.ReadDir(filepath.Join(root, "inputs"))
	if err != nil {
		return cfg, err
	}
	codecSet := map[stream.Codec]bool{}
	brSet := map[int]bool{}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".bits" || contains(name, ".truncated.") {
			continue
		}
		stem := name[:len(name)-len(".bits")]
		var codecStr string
		var br int
		if _, err := parseStem(stem, &codecStr, &br); err != nil {
			continue
		}
		codec := stream.Codec(codecStr)
		if codec.Valid() {
			codecSet[codec] = true
			brSet[br] = true
		}
	}
	for c := range codecSet {
		cfg.Codecs = append(cfg.Codecs, c)
	}
	sort.Slice(cfg.Codecs, func(i, j int) bool { return cfg.Codecs[i] < cfg.Codecs[j] })
	for b := range brSet {
		cfg.Bitrates = append(cfg.Bitrates, b)
	}
	sort.Ints(cfg.Bitrates)
	if len(cfg.Codecs) == 0 {
		cfg.Codecs = []stream.Codec{stream.CodecH264}
	}
	if len(cfg.Bitrates) == 0 {
		cfg.Bitrates = []int{500}
	}
	return cfg, nil
}

func parseStem(stem string, codec *string, br *int) (int, error) {
	// stem is "<codec>-<bitrate>"; split on the last hyphen.
	idx := -1
	for i := len(stem) - 1; i >= 0; i-- {
		if stem[i] == '-' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, os.ErrInvalid
	}
	*codec = stem[:idx]
	var n int
	for _, ch := range stem[idx+1:] {
		if ch < '0' || ch > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + int(ch-'0')
	}
	*br = n
	return 0, nil
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
