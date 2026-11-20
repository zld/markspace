package demux

import (
	"tsinspect/internal/fixture"
)

// patOnlyStream builds a minimal fixture with a valid PAT and no PMT, used to
// prove that slicing fails while a program's PMT is still pending.
func patOnlyStream() []byte {
	return fixture.PATOnlyBytes()
}
