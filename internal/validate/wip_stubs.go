package validate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"

	"pwflow/internal/ebml"
	"pwflow/internal/pipeline"
)

func newSHA() hash.Hash { return sha256.New() }
func hexSum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }
func checkAlignment(_ *Report, _ *pipeline.Manifest, _ *ebml.Report) {}
func checkVideo(_ context.Context, _ *Report, _ *pipeline.Manifest, _ string, _, _ int) {}
func checkAudio(_ context.Context, _ *Report, _ *pipeline.Manifest) {}
