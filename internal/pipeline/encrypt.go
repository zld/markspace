package pipeline

import (
	"mediakey/internal/crypto"
	"mediakey/internal/keys"
	"mediakey/internal/manifest"
)

// Encrypt splits plaintext into fixed-size chunks, rotates the dispatcher by
// chunk index and seals every chunk with AES-256-GCM. It returns the fully
// populated manifest; the root key never leaves the dispatcher.
func Encrypt(cfg EncryptConfig) (*manifest.Manifest, error) {
	chunks := len(cfg.Plaintext) / cfg.ChunkSize
	if len(cfg.Plaintext)%cfg.ChunkSize != 0 {
		chunks++
	}
	if err := sanityLimits(cfg.ChunkSize, chunks); err != nil {
		return nil, err
	}
	// Checked total-size guard: plaintext length already fits an int on
	// 64-bit platforms, but reject anything past the asset ceiling.
	if int64(len(cfg.Plaintext)) > manifest.MaxAssetBytes {
		return nil, manifest.NewError(manifest.ReasonLimitExceeded, "asset exceeds maximum size")
	}
	disp, err := keys.New(dispatcherRoot(cfg), cfg.RotateEvery)
	if err != nil {
		return nil, err
	}
	defer disp.Close()

	rng := defaultRand(cfg.Rand)
	m := &manifest.Manifest{
		Version: manifest.FormatVersion,
		Asset: manifest.Asset{
			ID:        cfg.AssetID,
			Size:      int64(len(cfg.Plaintext)),
			ChunkSize: cfg.ChunkSize,
		},
	}
	for idx := 0; idx < chunks; idx++ {
		start := idx * cfg.ChunkSize
		end := start + cfg.ChunkSize
		if end > len(cfg.Plaintext) {
			end = len(cfg.Plaintext)
		}
		plain := cfg.Plaintext[start:end]
		round, err := disp.EnsureForIndex(idx)
		if err != nil {
			return nil, err
		}
		key, keyID, err := disp.KeyForRound(round)
		if err != nil {
			return nil, err
		}
		env, err := crypto.Encrypt(rng, key, cfg.AssetID, idx, keyID, plain)
		if err != nil {
			return nil, err
		}
		m.Chunks = append(m.Chunks, manifest.Chunk{
			Index: idx, Offset: int64(start), Length: len(plain),
			Round: round, KeyID: keyID,
		})
		m.Envelopes = append(m.Envelopes, *env)
	}
	return m, nil
}

// dispatcherRoot keeps the root-key source injectable for tests while the
// normal path derives from the fixed fixture seed.
func dispatcherRoot(cfg EncryptConfig) []byte {
	if len(cfg.RootKey) == 32 {
		return cfg.RootKey
	}
	return fixtureRoot(cfg.AssetID)
}
