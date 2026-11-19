package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"mediakey/internal/fixture"
	"mediakey/internal/keys"
	"mediakey/internal/license"
	"mediakey/internal/manifest"
	"mediakey/internal/pipeline"
)

func runEncrypt(args []string) int {
	fs := flag.NewFlagSet("encrypt", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	input := fs.String("input", "", "plaintext file to encrypt (required)")
	output := fs.String("output", "", "manifest JSON output path (required)")
	chunkSize := fs.Int("chunk-size", 4096, "bytes per chunk")
	rotateEvery := fs.Int("rotate-every", 3, "rotate key every N chunks")
	asset := fs.String("asset", "local-asset-0001", "asset id bound to the envelopes")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *input == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "encrypt: -input and -output are required")
		return ExitUsage
	}
	if *chunkSize <= 0 || *rotateEvery <= 0 {
		fmt.Fprintln(os.Stderr, "encrypt: -chunk-size and -rotate-every must be positive")
		return ExitUsage
	}
	plain, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read input: %v\n", err)
		return ExitBadManifest
	}
	if len(plain) == 0 {
		fmt.Fprintln(os.Stderr, "encrypt: input file is empty")
		return ExitBadManifest
	}
	rootKey := keys.DeriveTestRootKey(
		append([]byte(fixture.RootSeedText), []byte(":"+*asset)...))
	m, err := pipeline.Encrypt(pipeline.EncryptConfig{
		AssetID:     *asset,
		ChunkSize:   *chunkSize,
		RotateEvery: *rotateEvery,
		Plaintext:   plain,
		RootKey:     rootKey,
	})
	if err != nil {
		return fail(err)
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fail(manifest.NewError(manifest.ReasonInvalidInput, err.Error()))
	}
	if err := os.WriteFile(*output, raw, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write output: %v\n", err)
		return ExitBadManifest
	}
	fmt.Printf("encrypted %d chunks -> %s\n", len(m.Chunks), *output)
	return ExitOK
}

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	manifestPath := fs.String("manifest", "", "manifest JSON produced by encrypt (required)")
	workers := fs.Int("workers", 1, "concurrent verification workers")
	ttl := fs.Int64("license-ttl", 3600, "seconds of default license validity from now")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "verify: -manifest is required")
		return ExitUsage
	}
	m, code := loadManifest(*manifestPath)
	if code != ExitOK {
		return code
	}
	rootKey := keys.DeriveTestRootKey(
		append([]byte(fixture.RootSeedText), []byte(":"+m.Asset.ID)...))
	signingKey := license.DeriveTestSigningKey([]byte(fixture.LicenseSeedText))

	maxRound := 0
	for _, c := range m.Chunks {
		if c.Round > maxRound {
			maxRound = c.Round
		}
	}
	now := time.Now().UTC()
	lic := &license.License{
		AssetID:   m.Asset.ID,
		MinRound:  0,
		MaxRound:  maxRound,
		MinChunk:  0,
		MaxChunk:  len(m.Chunks) - 1,
		NotBefore: now.Unix() - 60,
		NotAfter:  now.Unix() + *ttl,
	}
	if err := lic.Sign(signingKey); err != nil {
		return fail(err)
	}
	results, rotations, err := pipeline.Verify(pipeline.VerifyConfig{
		Manifest:    m,
		RootKey:     rootKey,
		SigningKey:  signingKey,
		Lic:         lic,
		Now:         now,
		GraceWindow: 0,
		Workers:     *workers,
		NonceFor:    nil, // crypto/rand nonces in real verification
	})
	if err != nil {
		return fail(err)
	}
	return emitReport(m.Asset.ID, rotations, results)
}
