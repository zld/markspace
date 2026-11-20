// Package hls builds master and media playlists plus the pipeline index.
// Serialization is canonical: fixed key order and LF line endings, so the
// validator can regenerate an expected playlist byte-for-byte and diff it.
package hls

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"hlscheck/internal/fsutil"
	"hlscheck/internal/model"
)

// Version is the HLS protocol version emitted in every playlist.
const Version = 3

// Index is the on-disk record of a complete slice run. It is the single
// source of truth the validator uses to rebuild expected playlists.
type Index struct {
	// TargetSeconds is the EXT-X-TARGETDURATION shared by all renditions.
	TargetSeconds int `json:"target_seconds"`
	// FrameRate is the source frame rate used to interpret tolerances.
	FrameRate int `json:"frame_rate"`
	// Renditions lists every variant in stable (bitrate ascending) order.
	Renditions []IndexRendition `json:"renditions"`
}

// IndexRendition is one variant entry inside the index.
type IndexRendition struct {
	Rendition model.Rendition `json:"rendition"`
	Segments  []model.Segment `json:"segments"`
	// Playlist is the safe relative path of the media playlist file.
	Playlist string `json:"playlist"`
}

// TargetDuration returns ceil(max segment duration) in whole seconds, per
// RFC 8216: EXT-X-TARGETDURATION must be an integer and at least the
// longest EXTINF when rounded to the nearest integer.
func TargetDuration(segments []model.Segment) int {
	maxMS := int64(0)
	for _, s := range segments {
		if d := s.DurationMS(); d > maxMS {
			maxMS = d
		}
	}
	whole := int((maxMS + 999) / 1000)
	if whole < 1 {
		whole = 1
	}
	return whole
}

// MediaPlaylist renders a VOD media playlist with an explicit end tag.
// Output is deterministic for a given segment list.
func MediaPlaylist(ir IndexRendition, targetSeconds int) []byte {
	var b bytes.Buffer
	fmt.Fprintln(&b, "#EXTM3U")
	fmt.Fprintf(&b, "#EXT-X-VERSION:%d\n", Version)
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetSeconds)
	fmt.Fprintln(&b, "#EXT-X-MEDIA-SEQUENCE:0")
	fmt.Fprintln(&b, "#EXT-X-PLAYLIST-TYPE:VOD")
	fmt.Fprintln(&b, "#EXT-X-INDEPENDENT-SEGMENTS")
	for _, s := range ir.Segments {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n", float64(s.DurationMS())/1000.0)
		fmt.Fprintln(&b, s.File)
	}
	fmt.Fprintln(&b, "#EXT-X-ENDLIST")
	return b.Bytes()
}

// MasterPlaylist renders the variant list ordered by ascending bandwidth so
// regeneration during validation is stable.
func MasterPlaylist(renditions []IndexRendition) []byte {
	ordered := make([]IndexRendition, len(renditions))
	copy(ordered, renditions)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Rendition.Bps < ordered[j].Rendition.Bps
	})
	var b bytes.Buffer
	fmt.Fprintln(&b, "#EXTM3U")
	fmt.Fprintf(&b, "#EXT-X-VERSION:%d\n", Version)
	fmt.Fprintln(&b, "#EXT-X-INDEPENDENT-SEGMENTS")
	for _, ir := range ordered {
		r := ir.Rendition
		// CODECS names the constrained-baseline H.264 + AAC pair produced by
		// the fixture generator; resolution is omitted as it is identical.
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=\"avc1.42E01E,mp4a.40.2\"\n", r.Bps)
		fmt.Fprintln(&b, ir.Playlist)
	}
	return b.Bytes()
}

// MarshalIndex serializes the index with stable key ordering via indentation.
func MarshalIndex(idx *Index) ([]byte, error) {
	return json.MarshalIndent(idx, "", "  ")
}

// WriteAll publishes every playlist and the index under root using atomic
// replaces. Relative playlist paths are validated against root first.
func WriteAll(root string, idx *Index) error {
	for _, ir := range idx.Renditions {
		path, err := fsutil.SafeJoinRel(root, ir.Playlist)
		if err != nil {
			return err
		}
		if err := fsutil.AtomicWriteFile(path, MediaPlaylist(ir, idx.TargetSeconds), 0o644); err != nil {
			return err
		}
	}
	masterPath, err := fsutil.SafeJoin(root, "index.m3u8")
	if err != nil {
		return err
	}
	if err := fsutil.AtomicWriteFile(masterPath, MasterPlaylist(idx.Renditions), 0o644); err != nil {
		return err
	}
	indexPath, err := fsutil.SafeJoin(root, "index.json")
	if err != nil {
		return err
	}
	data, err := MarshalIndex(idx)
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteFile(indexPath, append(data, '\n'), 0o644)
}
