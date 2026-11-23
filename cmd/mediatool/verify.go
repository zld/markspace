package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	clockrtp "mediatool/internal/rtp"
	"mediatool/internal/remux"
)

// runVerify reloads a previously written output directory and prints its
// reproducible summary hash. A missing or unreadable directory is an
// input failure (exit 1); bad arguments are exit 2.
func runVerify(args []string) int {
	dir, rest, err := flagString(args, "--dir", "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return 2
	}
	for _, a := range rest {
		fmt.Fprintf(os.Stderr, "argument error: unexpected argument %q\n", a)
		return 2
	}
	if dir == "" {
		fmt.Fprintln(os.Stderr, "argument error: --dir is required")
		return 2
	}
	manifest, err := remux.LoadManifest(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "processing failure: cannot load manifest: %v\n", err)
		return 1
	}
	// Re-derive the summary hash from the canonical content and confirm it
	// matches the stored value, proving the manifest was not tampered with.
	recomputed := recomputeSummaryHash(manifest)
	fmt.Printf("summary_hash=%s\n", manifest.SummaryHash)
	fmt.Printf("recomputed_hash=%s\n", recomputed)
	fmt.Printf("hash_match=%t\n", recomputed == manifest.SummaryHash)
	fmt.Printf("slots=%d\n", len(manifest.Slots))
	var chunks int
	for _, s := range manifest.Slots {
		chunks += len(s.Fragments)
	}
	fmt.Printf("chunks=%d\n", chunks)
	if recomputed != manifest.SummaryHash {
		fmt.Fprintln(os.Stderr, "processing failure: summary hash mismatch")
		return 1
	}
	// Echo the demo summary when present, so verify is a one-shot audit.
	if raw, rerr := os.ReadFile(joinSafe(dir, "summary.txt")); rerr == nil {
		fmt.Print(string(raw))
	}
	return 0
}

// recomputeSummaryHash mirrors remux.Manifest's canonical hashing using
// the exported canonical serialization so results stay byte-identical.
func recomputeSummaryHash(m *remux.Manifest) string {
	raw, err := m.CanonicalJSON()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func joinSafe(dir, name string) string {
	p, err := remux.SafePath(dir, name)
	if err != nil {
		return name
	}
	return p
}

// parseRTPTimestamp decodes an RTP packet and returns its raw 32-bit
// timestamp; the pipeline's clock layer performs the unwrapping.
func parseRTPTimestamp(buf []byte) (*clockrtp.Packet, uint32, error) {
	return clockrtp.ParsePacket(buf)
}
