package pipeline

import (
	"time"

	"mediakey/internal/fixture"
	"mediakey/internal/license"
	"mediakey/internal/manifest"
)

// DemoConfig controls the reproducible end-to-end demo.
type DemoConfig struct {
	Chunks       int
	ChunkSize    int
	RotateEvery  int
	Injections   map[string]bool
	Workers      int
	GraceSeconds int64
}

// Demo generates deterministic content, encrypts it, builds a signed license
// and verifies every chunk with the requested fault injections. It returns
// the per-chunk results and the rotation count for the report layer.
func Demo(cfg DemoConfig) ([]manifest.ChunkResult, int, *manifest.Manifest, error) {
	plan := fixture.Plan{
		AssetID:      "demo-asset-0001",
		Chunks:       cfg.Chunks,
		ChunkSize:    cfg.ChunkSize,
		RotateEvery:  cfg.RotateEvery,
		GraceSeconds: cfg.GraceSeconds,
		Injections:   cfg.Injections,
	}
	sc, err := fixture.New(plan)
	if err != nil {
		return nil, 0, nil, err
	}
	sc.PlaceFaults()

	m, err := Encrypt(EncryptConfig{
		AssetID:     plan.AssetID,
		ChunkSize:   cfg.ChunkSize,
		RotateEvery: cfg.RotateEvery,
		Plaintext:   sc.Plaintext,
		RootKey:     sc.RootKey,
	})
	if err != nil {
		return nil, 0, nil, err
	}

	maxRound := 0
	for _, c := range m.Chunks {
		if c.Round > maxRound {
			maxRound = c.Round
		}
	}
	lic := &license.License{
		AssetID:   plan.AssetID,
		MinRound:  0,
		MaxRound:  maxRound,
		MinChunk:  0,
		MaxChunk:  cfg.Chunks - 1,
		NotBefore: fixture.BaseTime,
		NotAfter:  sc.LicenseExpiry,
		GraceWindow: cfg.GraceSeconds,
	}
	if err := lic.Sign(sc.SigningKey); err != nil {
		return nil, 0, nil, err
	}

	results, rotations, err := Verify(VerifyConfig{
		Manifest:    m,
		RootKey:     sc.RootKey,
		SigningKey:  sc.SigningKey,
		Lic:         lic,
		Now:         time.Unix(sc.Now, 0).UTC(),
		GraceWindow: time.Duration(cfg.GraceSeconds) * time.Second,
		Workers:     cfg.Workers,
		Faults:      sc.Faults,
		NonceFor:    func(idx int) string { return fixture.NonceFor(plan.AssetID, idx) },
	})
	if err != nil {
		return nil, 0, nil, err
	}
	return results, rotations, m, nil
}
