package manifest

// Validate performs structural and limit checks on a manifest. It does not
// touch any key material or plaintext.
func (m *Manifest) Validate() error {
	if m == nil {
		return NewError(ReasonInvalidInput, "nil manifest")
	}
	if m.Version != FormatVersion {
		return NewError(ReasonInvalidInput, "unsupported manifest version")
	}
	if m.Asset.ID == "" {
		return NewError(ReasonInvalidInput, "missing asset id")
	}
	if m.Asset.ChunkSize <= 0 || m.Asset.ChunkSize > MaxChunkBytes {
		return NewError(ReasonLimitExceeded, "chunk size out of allowed range")
	}
	if m.Asset.Size < 0 || m.Asset.Size > MaxAssetBytes {
		return NewError(ReasonLimitExceeded, "asset size out of allowed range")
	}
	if len(m.Chunks) == 0 || len(m.Chunks) > MaxChunks {
		return NewError(ReasonLimitExceeded, "chunk count out of allowed range")
	}
	if len(m.Chunks) != len(m.Envelopes) {
		return NewError(ReasonInvalidInput, "chunk/envelope count mismatch")
	}
	// Indices must be a dense 0..n-1 sequence; duplicates and gaps are
	// rejected so downstream processing can rely on stable ordering.
	seen := make(map[int]bool, len(m.Chunks))
	for i, c := range m.Chunks {
		if c.Index != i {
			return NewError(ReasonChunkReordered, "chunk indices must be dense and ordered")
		}
		if seen[c.Index] {
			return NewError(ReasonChunkDuplicate, "duplicate chunk index")
		}
		seen[c.Index] = true
		if c.Length <= 0 || c.Length > MaxChunkBytes {
			return NewError(ReasonLimitExceeded, "chunk length out of range")
		}
		if c.KeyID == "" {
			return NewError(ReasonInvalidInput, "missing key id")
		}
		e := m.Envelopes[i]
		if e.ChunkIndex != c.Index || e.AssetID != m.Asset.ID || e.KeyID != c.KeyID {
			return NewError(ReasonInvalidInput, "envelope metadata mismatch")
		}
	}
	return nil
}
