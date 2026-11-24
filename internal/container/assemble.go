package container

import "github.com/zld/mediarc/internal/model"

// assembleTracks creates one Track value per scanned trak box. Timestamp,
// keyframe, and metadata enrichment are added by the following batch.
func (f *File) assembleTracks(_ model.Config) error {
	trackCount := 0
	for _, b := range f.Boxes {
		if b.TrackIndex+1 > trackCount {
			trackCount = b.TrackIndex + 1
		}
	}
	f.Tracks = make([]*Track, 0, trackCount)
	for i := 0; i < trackCount; i++ {
		f.Tracks = append(f.Tracks, &Track{})
	}
	return nil
}
