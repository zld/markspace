package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"

	"archive/internal/model"
	"archive/internal/parse"
)

func listInputs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext == ".mp4" || ext == ".mxf" {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

func hashInputs(files []string) (string, error) {
	h := sha256.New()
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			return "", err
		}
		if st.Size() > model.MaxInputBytes {
			return "", model.ParseFault(0, -1, "input %s exceeds 2 GiB limit", f)
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		h.Write([]byte(filepath.Base(f)))
		h.Write([]byte{0})
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func parseAll(files []string) ([]*parse.Track, error) {
	tracks := make([]*parse.Track, 0, len(files))
	for _, f := range files {
		var tr *parse.Track
		var err error
		switch filepath.Ext(f) {
		case ".mp4":
			tr, err = parse.ParseMP4File(f)
		case ".mxf":
			tr, err = parse.ParseMXFFile(f)
		}
		if err != nil {
			return nil, err
		}
		tr.Source = filepath.Base(f) // stable, directory-independent
		tracks = append(tracks, tr)
	}
	return tracks, nil
}

func readPayloads(inDir string, tr *parse.Track) ([][]byte, error) {
	path, err := CheckRelPath(inDir, tr.Source)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	payloads := make([][]byte, len(tr.Samples))
	for i, s := range tr.Samples {
		if s.Offset < 0 || s.Offset+int64(s.Size) > int64(len(data)) {
			return nil, model.StageFault(model.StageRender, i, s.Offset, nil,
				"sample payload out of bounds in %s", tr.Source)
		}
		payloads[i] = data[s.Offset : s.Offset+int64(s.Size)]
	}
	return payloads, nil
}
