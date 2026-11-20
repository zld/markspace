// Command apply-vendor-patch re-applies the rtspingest modification to the
// vendored gortsplib server-side UDP packet path.
//
// `go mod vendor` always refreshes vendor/ from the pristine module, wiping
// the local hunk. This tool is idempotent: it detects whether the patch is
// present and only rewrites the file when the pristine function is found.
// Run it after every `go mod vendor`:
//
//	go run ./tools/apply-vendor-patch
package main

import (
	"fmt"
	"os"
	"strings"
)

const target = "vendor/github.com/bluenviron/gortsplib/v4/server_session_format.go"

const marker = "[rtspingest vendor patch]"

const pristine = `func (sf *serverSessionFormat) readPacketRTPUDP(pkt *rtp.Packet, now time.Time) {
	packets, lost := sf.udpReorderer.Process(pkt)
	if lost != 0 {
		sf.onPacketRTPLost(lost)
		// do not return
	}

	for _, pkt := range packets {
		sf.handlePacketRTP(pkt, now)
	}
}`

const patched = `func (sf *serverSessionFormat) readPacketRTPUDP(pkt *rtp.Packet, now time.Time) {
	// [rtspingest vendor patch] bypass the internal 64-packet reorder queue
	// and the library loss reporting entirely. The ingest tool performs its
	// own sequence reordering, gap and duplicate accounting in
	// internal/rtp (wraparound-aware), so packets must reach the
	// OnPacketRTP callback in raw arrival order with nothing dropped.
	sf.handlePacketRTP(pkt, now)
}`

func main() {
	raw, err := os.ReadFile(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply-vendor-patch: read %s: %v\n", target, err)
		os.Exit(1)
	}
	src := string(raw)

	switch {
	case strings.Contains(src, marker):
		fmt.Println("apply-vendor-patch: already applied")
		return
	case strings.Contains(src, pristine):
		src = strings.Replace(src, pristine, patched, 1)
		if err := os.WriteFile(target, []byte(src), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "apply-vendor-patch: write: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("apply-vendor-patch: applied")
	default:
		fmt.Fprintln(os.Stderr, "apply-vendor-patch: neither pristine nor patched function found; "+
			"upstream gortsplib may have changed this file")
		os.Exit(1)
	}
}
