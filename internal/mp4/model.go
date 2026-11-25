package mp4

import "mediacore/internal/clock"

// Box is a minimally decoded ISO-BMFF box header and tree reference.
type Box struct {
	Type          string
	Offset        int64
	HeaderSize    int64
	PayloadOffset int64
	Size          int64
	Track         int
	Children      []*Box
}

// Sample is one expanded sample-table entry with its original file location.
type Sample struct {
	Number int    `json:"number"`
	DTS    int64  `json:"dts"`
	PTS    int64  `json:"pts"`
	Delta  uint32 `json:"delta"`
	Size   uint32 `json:"size"`
	Offset int64  `json:"raw_offset"`
	Sync   bool   `json:"sync"`
}

// Track contains the metadata needed to build a random-access index.
type Track struct {
	Path        string
	Number      int
	ID          uint32
	TimeScale   clock.TicksPerSecond
	MovieScale  clock.TicksPerSecond
	Duration    uint64
	Edits       []clock.EditEntry
	HasEdit     bool
	Samples     []Sample
	SyncNumbers []uint32
}

// File is the parsed MP4 container.
type File struct {
	Path           string
	MovieTimeScale clock.TicksPerSecond
	MovieDuration  uint64
	Tracks         []*Track
	SizeBytes      int64
	root           *Box
	fileSize       int64
}

// Size returns the container size observed when it was parsed.
func (f *File) Size() int64 { return f.fileSize }

func (b *Box) findChild(want string) *Box {
	if b == nil {
		return nil
	}
	for _, c := range b.Children {
		if c.Type == want {
			return c
		}
	}
	return nil
}

func (b *Box) findPath(types ...string) *Box {
	cur := b
	for _, typ := range types {
		cur = cur.findChild(typ)
		if cur == nil {
			return nil
		}
	}
	return cur
}

func (b *Box) findAll(want string) []*Box {
	var out []*Box
	var walk func(*Box)
	walk = func(x *Box) {
		for _, c := range x.Children {
			if c.Type == want {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(b)
	return out
}
