package pipeline

import (
	"fmt"
	"testing"
)

func TestDebugAVSync(t *testing.T) {
	var av avSync
	// 复现排序后序列：c3 audio@2000, c4 audio(corrected)@2001, video@2001, audio@2021, video@2041, audio@2041
	seq := []struct{ typ string; ts int64 }{
		{"a", 2000e6}, {"a", 2001e6}, {"v", 2001e6}, {"a", 2021e6}, {"v", 2041e6}, {"a", 2041e6},
	}
	for _, s := range seq {
		if s.typ == "a" { av.onAudio(s.ts) } else { av.onVideo(s.ts) }
	}
	av.finish()
	fmt.Println("maxdiff ms:", float64(av.maxDiff)/1e6)
}
