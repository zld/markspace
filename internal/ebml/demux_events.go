// Package ebml implements a bounded, streaming reader for the subset of EBML
// used by Matroska and WebM. This file tracks master-element nesting.
package ebml

import "mediad/internal/merr"

func (d *Demuxer) start(ev Event) error {
	switch ev.ID {
	case IDSegment:
		if d.inCluster == 0 && d.inTrack == 0 && d.inInfo == 0 {
			// First segment: nothing to do; nested segments are rejected by
			// the walker's child rules.
		}
	case IDInfo:
		d.inInfo++
	case IDTracks:
		d.inTracks++
	case IDTrackEntry:
		d.inTrack++
		d.curTrack = TrackInfo{}
	case IDCluster:
		if d.inCluster > 0 {
			return merr.New("demux.cluster", merr.CodeBadInput, ev.HeaderOffset,
				"nested Cluster is not valid Matroska")
		}
		d.inCluster++
		d.clusterTC = 0
		d.clusterOff = ev.HeaderOffset
		d.info.ClusterEvents++
	}
	return nil
}

func (d *Demuxer) end(ev Event) error {
	switch ev.ID {
	case IDInfo:
		d.inInfo--
	case IDTracks:
		d.inTracks--
	case IDTrackEntry:
		if d.inTrack > 0 {
			d.inTrack--
			if d.curTrack.Number != 0 {
				d.info.Tracks = append(d.info.Tracks, d.curTrack)
			}
		}
	case IDCluster:
		if d.inCluster > 0 {
			d.inCluster--
		}
	}
	return nil
}
