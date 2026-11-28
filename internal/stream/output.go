package stream

import (
	"os"
	"path/filepath"
	"strconv"

	"avpipe/internal/model"
	"avpipe/internal/repair"
	"avpipe/internal/scan"
)

// outputName is the per-track converted-output file name inside state dir.
func outputName(track int) string {
	return "track" + pad2(track) + ".out"
}

func pad2(v int) string {
	if v < 10 {
		return "0" + strconv.Itoa(v)
	}
	return strconv.Itoa(v)
}

func jsonNumber[T ~int64](v T) string {
	return strconv.FormatInt(int64(v), 10)
}

// finalizeOutputs writes one deterministic, length-prefixed output per track
// and returns the map output-name -> content hash stored in the manifest.
func finalizeOutputs(stateDir string, rep *repair.Report) (map[string]string, error) {
	outputs := map[string]string{}
	for i := range rep.Tracks {
		tr := &rep.Tracks[i]
		name := outputName(tr.Track)
		var content []byte
		for _, s := range tr.Samples {
			record := sampleContent(s)
			content = append(content, byte(len(record)>>24), byte(len(record)>>16),
				byte(len(record)>>8), byte(len(record)))
			content = append(content, record...)
		}
		if err := os.WriteFile(filepath.Join(stateDir, name), content, 0o644); err != nil {
			return nil, model.NewError(model.CodeIO, false, "write output: %v", err)
		}
		outputs[name] = scan.HashBytes(content)
	}
	return outputs, nil
}
