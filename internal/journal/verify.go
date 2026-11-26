package journal

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"continuity/internal/media"
)

// ChecksumSummary builds SHA256SUMS content for frame artifacts.
func ChecksumSummary(artifacts []Artifact) Artifact {
	names := make([]string, 0, len(artifacts))
	byName := map[string]Artifact{}
	for _, a := range artifacts {
		if strings.HasPrefix(a.Name, "frames/") {
			names = append(names, a.Name)
			byName[a.Name] = a
		}
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		a := byName[name]
		fmt.Fprintf(&b, "%s  %s\n", a.SHA, name)
	}
	return Artifact{Name: checksumsFile, Data: []byte(b.String())}
}

func fileMatches(path, expectedSHA string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fault(media.CodeHashMismatch, path, err)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != expectedSHA {
		return true, &media.Fault{Phase: media.PhaseJournal, Code: media.CodeHashMismatch, Source: path,
			Detail: fmt.Sprintf("existing content hash %s != expected %s; stopping without overwrite", got, expectedSHA)}
	}
	return true, nil
}

func recoverPendingChecksums(dir string) (bool, error) {
	p := filepath.Join(dir, checksumsFile)
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fault(media.CodeHashMismatch, p, err)
	}
	defer f.Close()
	pending := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return true, &media.Fault{Phase: media.PhaseJournal, Code: media.CodeHashMismatch, Source: p, Detail: "bad checksum line"}
		}
		sha, name := fields[0], fields[1]
		ok, err := fileMatches(filepath.Join(dir, filepath.FromSlash(name)), sha)
		if err != nil {
			return true, err
		}
		if ok {
			pending = true
		}
	}
	return pending, sc.Err()
}

func loadStateIfPresent(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fault(media.CodePublication, path, err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, &media.Fault{Phase: media.PhaseJournal, Code: media.CodePublication, Source: path, Err: err}
	}
	return &st, nil
}

func verifyCommitted(dir string, st *State, artifacts []Artifact) error {
	byName := map[string]string{}
	for _, a := range artifacts {
		byName[a.Name] = a.SHA
	}
	for _, fr := range st.Frames {
		name := fr.File
		expected := fr.SHA256
		if want, ok := byName[name]; ok && want != "" {
			expected = want
		}
		if _, err := fileMatches(filepath.Join(dir, filepath.FromSlash(name)), expected); err != nil {
			return err
		}
	}
	if len(st.Frames) != st.FrameCount {
		return &media.Fault{Phase: media.PhaseJournal, Code: media.CodePublication,
			Source: filepath.Join(dir, stateFile), Detail: "frame_count does not match records"}
	}
	return nil
}
