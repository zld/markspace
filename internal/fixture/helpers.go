package fixture

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"strings"

	"mediakey/internal/manifest"
)

func hmacSHA256(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

// KnownInjections is the set the CLI normalizes user input against.
var KnownInjections = map[string]bool{
	InjectExpired: true,
	InjectReplay:  true,
	InjectTamper:  true,
	InjectWindow:  true,
}

// ParseInjections normalizes a comma-separated injection list. Empty entries
// are ignored; unknown names produce a scriptable invalid-input error.
func ParseInjections(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(strings.ToLower(part))
		if name == "" {
			continue
		}
		if !KnownInjections[name] {
			return nil, manifest.NewError(manifest.ReasonInvalidInput, "unknown injection: "+name)
		}
		out[name] = true
	}
	return out, nil
}

// NonceFor returns a deterministic 16-byte request nonce for a chunk. The
// pipeline uses these in the demo so replay attacks can be reproduced
// exactly; production callers pass crypto/rand instead.
func NonceFor(assetID string, chunkIndex int) string {
	var b [8]byte
	binary.BigEndian.PutUint32(b[:4], uint32(chunkIndex))
	binary.BigEndian.PutUint32(b[4:], 0x4d4b3100) // "MK1\0"
	mac := hmacSHA256([]byte(RootSeedText+":nonce"), append([]byte(assetID), b[:]...))
	return string(mac[:16])
}

// TamperEnvelope flips one ciphertext byte so GCM authentication must fail.
// It mutates the envelope in place and returns the same pointer.
func TamperEnvelope(env *manifest.Envelope) *manifest.Envelope {
	if len(env.Ciphertext) == 0 {
		return env
	}
	env.Ciphertext[0] ^= 0xFF
	return env
}
