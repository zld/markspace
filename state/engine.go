package state

import (
	"archivecheck/cache"
	"archivecheck/checkpoint"
	"archivecheck/manifest"
	"archivecheck/pipe"
	rtpingest "archivecheck/rtp"
)

type engine struct {
	cfg     Config
	root    string
	budget  *pipe.Budget
	cas     *cache.CAS
	journal *checkpoint.Journal

	dash   *manifest.DASHManifest
	hls    *manifest.HLSManifest
	events []rtpingest.Event

	inputDigest string
	cp          checkpoint.Checkpoint
	prior       *State
	tracker     *rtpingest.Tracker

	work      []workItem
	byCursor  map[int64]workItem
	items     []ArchiveItem
	itemIndex map[int64]int

	audioPTS []int64
	videoPTS []int64

	badSegments  int
	fallback     int
	unresolved   int
	hlsNext      int
	dashResolved int
}
