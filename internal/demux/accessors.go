package demux

import "mediapack/internal/model"

// TrackMeta returns the metadata of a track by id, or a zero-valued
// TrackMeta with Timescale 0 when the track is absent.
func (c *Container) TrackMeta(tid uint32) model.TrackMeta {
	return c.meta(tid)
}

// HasTrack reports whether the container declares the given track id.
func (c *Container) HasTrack(tid uint32) bool {
	for i := range c.Tracks {
		if c.Tracks[i].TrackID == tid {
			return true
		}
	}
	return false
}
