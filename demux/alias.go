package demux

import "github.com/abema/go-mp4"

// Thin aliases keep the sample-table code independent of the concrete
// go-mp4 struct names and make table-driven tests simple to construct.
type (
	mp4Stts = mp4.Stts
	mp4Stsz = mp4.Stsz
	mp4Stss = mp4.Stss
)
