package manifest

// Safety limits. Inputs beyond these are rejected up-front so that malformed
// manifests cannot trigger unbounded memory allocation or integer overflow.
const (
	MaxAssetBytes  = 256 << 20 // 256 MiB of plaintext per asset
	MaxChunks      = 4096
	MaxChunkBytes  = 1 << 20 // 1 MiB per chunk
	MaxCacheEntries = 1 << 16 // replay cache capacity ceiling
	MaxKeyRounds   = 256
	FormatVersion  = 1
)

// Asset describes one piece of source media before encryption.
type Asset struct {
	ID        string `json:"id"`
	Size      int64  `json:"size"`
	ChunkSize int    `json:"chunk_size"`
}

// Chunk records where a plaintext slice lives and which key round encrypted
// it. Plaintext offsets are int64 with checked arithmetic at the call sites.
type Chunk struct {
	Index  int    `json:"index"`
	Offset int64  `json:"offset"`
	Length int    `json:"length"`
	Round  int    `json:"round"`
	KeyID  string `json:"key_id"`
}

// Envelope is the encrypted container for one chunk. It contains every field
// the format requires: version, asset ID, chunk index, key ID, nonce and the
// authenticated ciphertext (GCM tag appended by crypto/cipher).
type Envelope struct {
	Version    int    `json:"version"`
	AssetID    string `json:"asset_id"`
	ChunkIndex int    `json:"chunk_index"`
	KeyID      string `json:"key_id"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// Manifest binds an asset, its chunk plan and the encrypted envelopes.
type Manifest struct {
	Version  int        `json:"version"`
	Asset    Asset      `json:"asset"`
	Chunks   []Chunk    `json:"chunks"`
	Envelopes []Envelope `json:"envelopes"`
}

// ChunkResult is the per-chunk verdict. Every chunk produces exactly one
// result, even when an earlier chunk failed, so no failure can be masked.
type ChunkResult struct {
	ChunkIndex    int           `json:"chunk_index"`
	KeyID         string        `json:"key_id"`
	Status        string        `json:"status"`
	FailureReason FailureReason `json:"failure_reason"`
	Detail        string        `json:"detail,omitempty"`
}

// Summary aggregates counts across the whole run.
type Summary struct {
	Total   int            `json:"total"`
	OK      int            `json:"ok"`
	Failed  int            `json:"failed"`
	Reasons map[string]int `json:"reasons"`
}
