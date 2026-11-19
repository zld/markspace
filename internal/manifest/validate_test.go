package manifest

import "testing"

func validManifest() *Manifest {
	m := &Manifest{
		Version: FormatVersion,
		Asset:   Asset{ID: "a", Size: 128, ChunkSize: 64},
	}
	for i := 0; i < 2; i++ {
		m.Chunks = append(m.Chunks, Chunk{
			Index: i, Offset: int64(i * 64), Length: 64,
			Round: 0, KeyID: "key-r000",
		})
		m.Envelopes = append(m.Envelopes, Envelope{
			Version: FormatVersion, AssetID: "a",
			ChunkIndex: i, KeyID: "key-r000",
		})
	}
	return m
}

func TestValidManifest(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestInvalidManifests(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Manifest)
		want FailureReason
	}{
		{"bad version", func(m *Manifest) { m.Version = 99 }, ReasonInvalidInput},
		{"missing asset", func(m *Manifest) { m.Asset.ID = "" }, ReasonInvalidInput},
		{"chunk size zero", func(m *Manifest) { m.Asset.ChunkSize = 0 }, ReasonLimitExceeded},
		{"no chunks", func(m *Manifest) { m.Chunks = nil; m.Envelopes = nil }, ReasonLimitExceeded},
		{"count mismatch", func(m *Manifest) { m.Envelopes = m.Envelopes[:1] }, ReasonInvalidInput},
		{"gap index", func(m *Manifest) { m.Chunks[1].Index = 7 }, ReasonChunkReordered},
		{"missing key id", func(m *Manifest) { m.Chunks[0].KeyID = "" }, ReasonInvalidInput},
		{"oversize asset", func(m *Manifest) { m.Asset.Size = MaxAssetBytes + 1 }, ReasonLimitExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validManifest()
			tc.mut(m)
			if code := ErrorCode(m.Validate()); code != tc.want {
				t.Fatalf("code = %s, want %s", code, tc.want)
			}
		})
	}
}

func TestErrorCodeFallback(t *testing.T) {
	if ErrorCode(nil) != ReasonNone {
		t.Fatal("nil error must map to ReasonNone")
	}
}
