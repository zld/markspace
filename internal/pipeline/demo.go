package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"media-archiver/internal/media"
	"media-archiver/internal/samplegen"
)

// PrepareDemoInput generates the deterministic local WAV and expected-position file.
func PrepareDemoInput(workdir string, seed int64, injection string) (*samplegen.Expected, error) {
	if injection == "bad-index" {
		injection = "none"
	}
	path := filepath.Join(workdir, "input", "sample.wav")
	gen, err := samplegen.Generate(samplegen.Options{
		Seed: seed, Injection: injection, OutputPath: path,
	})
	if err != nil {
		return nil, err
	}
	expected := gen.Expected
	if injection == "" || injection == "normal" || injection == "none" {
		expected.TargetFrame = 6*samplegen.DemoBlockFrames + 64
		expected.ExpectedBlock = 6
		expected.ExpectedFrameOffset = 64
	}
	data, err := media.MarshalCanonicalJSON(expected)
	if err != nil {
		return nil, err
	}
	if err := media.WriteFileAtomic(filepath.Join(workdir, "expected.json"), data, 0o644); err != nil {
		return nil, err
	}
	return &expected, nil
}

// CorruptIndexForDemo appends invalid bytes to an existing index artifact.
func CorruptIndexForDemo(workdir string) error {
	path := artifactPath(workdir, indexPath)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(`{"corrupt":true}`)
	return err
}

// LoadExpected reads the generated fixture's expected lookup positions.
func LoadExpected(workdir string) (*samplegen.Expected, error) {
	data, err := os.ReadFile(filepath.Join(workdir, "expected.json"))
	if err != nil {
		return nil, err
	}
	var expected samplegen.Expected
	if err := json.Unmarshal(data, &expected); err != nil {
		return nil, err
	}
	return &expected, nil
}
