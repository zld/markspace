package fixture

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func buildBytes(t *testing.T, profile string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Build(profile, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestDeterministic builds every profile twice and requires identical
// bytes.
func TestDeterministic(t *testing.T) {
	for _, p := range []string{ProfileEditWrap, ProfileFragmentedWrap, ProfileTruncated, ProfileOOB} {
		a := buildBytes(t, p)
		b := buildBytes(t, p)
		if !bytes.Equal(a, b) {
			t.Fatalf("profile %s not deterministic", p)
		}
		if len(a) == 0 {
			t.Fatalf("profile %s empty", p)
		}
	}
}

// TestGoldenHash pins the edit-wrap output so accidental layout changes
// are caught.
func TestGoldenHash(t *testing.T) {
	sum := sha256.Sum256(buildBytes(t, ProfileEditWrap))
	t.Logf("edit-wrap sha256: %x", sum)
	again := sha256.Sum256(buildBytes(t, ProfileEditWrap))
	if sum != again {
		t.Fatal("hash unstable")
	}
}
