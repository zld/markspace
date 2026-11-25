package segment

import (
	"testing"

	"github.com/pion/rtp"
)

// mustRewriteTS unmarshals a packet, overwrites its RTP timestamp and
// re-marshals it, so tests can inject a timestamp rollback.
func mustRewriteTS(t *testing.T, raw []byte, ts uint32) []byte {
	t.Helper()
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	pkt.Timestamp = ts
	out, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return out
}
