package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediarun/internal/fault"
)

const stagesDir = "stages"

// unsupportedExt maps container extensions we explicitly refuse. DASH
// fragmented MP4 (.mp4/.m4s) and HLS MPEG-TS (.ts) are supported; anything
// else in the common container list is reported as unsupported rather than
// misparsed.
var unsupportedExt = map[string]bool{
	".mkv": true, ".webm": true, ".avi": true, ".mov": true,
	".wmv": true, ".flv": true, ".mpg": true, ".mpeg": true,
}

// checkDirs resolves and validates the input/output directories: the
// input must exist, the output is created, and the output must not be
// the input or inside it (inputs stay read-only).
func checkDirs(inputDir, outDir string) (string, string, error) {
	inAbs, err := filepath.Abs(inputDir)
	if err != nil {
		return "", "", fault.New(fault.StageInput, "bad input path").Wrap(err)
	}
	st, err := os.Stat(inAbs)
	if err != nil || !st.IsDir() {
		return "", "", fault.New(fault.StageInput, "input is not a directory").At(inAbs, -1)
	}
	outAbs, err := filepath.Abs(outDir)
	if err != nil {
		return "", "", fault.New(fault.StageInput, "bad output path").Wrap(err)
	}
	if outAbs == inAbs || strings.HasPrefix(outAbs, inAbs+string(os.PathSeparator)) {
		return "", "", fault.New(fault.StageInput,
			"output directory must not be the input directory or inside it").At(outAbs, -1)
	}
	if err := os.MkdirAll(outAbs, 0o755); err != nil {
		return "", "", fault.New(fault.StageCommit, "cannot create output directory").
			At(outAbs, -1).Wrap(err)
	}
	if err := os.MkdirAll(filepath.Join(outAbs, stagesDir), 0o755); err != nil {
		return "", "", fault.New(fault.StageCommit, "cannot create stages directory").
			At(outAbs, -1).Wrap(err)
	}
	return inAbs, outAbs, nil
}

// cleanTemps removes uncommitted temporary files from previous runs.
func cleanTemps(outDir string) error {
	var matches []string
	for _, dir := range []string{outDir, filepath.Join(outDir, stagesDir)} {
		matches = append(matches, mustGlob(filepath.Join(dir, ".tmp-*"))...)
		matches = append(matches, mustGlob(filepath.Join(dir, "*.tmp"))...)
	}
	for _, m := range matches {
		if err := os.RemoveAll(m); err != nil {
			return fault.New(fault.StageCommit, "cannot clean temporary file").At(m, -1).Wrap(err)
		}
	}
	return nil
}

func mustGlob(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	return m
}

// digestInputs hashes every regular file under the input directory
// recursively (relative, slash-separated names pin provenance) and returns
// the combined input digest plus the top-level manifest file list. Known
// unsupported containers are rejected explicitly. Hashing the whole tree
// means changing any input byte — including a nested boundary fixture —
// changes the idempotency key.
func digestInputs(inDir string) (string, []string, error) {
	h := sha256.New()
	var manifests []string
	walkErr := filepath.WalkDir(inDir, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return fault.New(fault.StageInput, "cannot inspect input path").
				At(path, -1).Wrap(werr)
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(inDir, path)
		if err != nil {
			return fault.New(fault.StageInput, "cannot resolve input path").At(path, -1).Wrap(err)
		}
		rel = filepath.ToSlash(rel)
		ext := strings.ToLower(filepath.Ext(rel))
		if unsupportedExt[ext] {
			return fault.New(fault.StageInput,
				fmt.Sprintf("unsupported container %q", rel)).At(path, -1)
		}
		f, err := os.Open(path)
		if err != nil {
			return fault.New(fault.StageInput, "cannot read input").At(path, -1).Wrap(err)
		}
		fh := sha256.New()
		_, copyErr := io.Copy(fh, f)
		f.Close()
		if copyErr != nil {
			return fault.New(fault.StageInput, "cannot read input").At(path, -1).Wrap(copyErr)
		}
		fmt.Fprintf(h, "%s\x00%x\x00", rel, fh.Sum(nil))
		if !strings.Contains(rel, "/") && (ext == ".mpd" || ext == ".m3u8") {
			manifests = append(manifests, rel)
		}
		return nil
	})
	if walkErr != nil {
		return "", nil, walkErr
	}
	sort.Strings(manifests)
	if len(manifests) == 0 {
		return "", nil, fault.New(fault.StageInput, "no .mpd or .m3u8 manifest in input").
			At(inDir, -1)
	}
	return hex.EncodeToString(h.Sum(nil)), manifests, nil
}
