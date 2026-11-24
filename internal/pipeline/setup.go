package pipeline

import (
	"errors"
	"os"

	"medpipe/internal/clock"
	"medpipe/internal/sample"
)

// SetupFresh generates the self-contained source and writes it only when it
// does not already exist. Existing input is never overwritten.
func SetupFresh(opts Options, cfg sample.Config) (*Runner, *sample.Generated, error) {
	gen, err := sample.Generate(cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, nil, err
	}
	p := sourcePath(opts.OutDir)
	if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
		if err := clock.AtomicWriteFile(p, gen.Data, 0o644); err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, err
	}
	digest, err := HashFile(p)
	if err != nil {
		return nil, nil, err
	}
	return NewRunner(opts, cfg, digest), gen, nil
}
