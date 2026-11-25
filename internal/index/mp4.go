package index

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	mp4 "github.com/abema/go-mp4"

	"medpipe/internal/fault"
	"medpipe/internal/ingest"
)

// MP4TrackMetadata contains the two boxes needed by this project's bounded
// MP4 metadata contract: a visual sample description and sync-sample index.
type MP4TrackMetadata struct {
	TrackID           uint16
	SampleDescription []byte
	SyncSampleBox     []byte
	SampleNumbers     []uint32
}

// MP4Metadata is returned by PackageMP4Metadata.
type MP4Metadata struct {
	Tracks map[uint16]MP4TrackMetadata
}

// MarshalVisualSampleDescription uses go-mp4 to encode an avc1-style visual
// sample entry carrying the validated dimensions. Medpipe does not encode
// video elementary streams; this is sample-description metadata only.
func MarshalVisualSampleDescription(f *ingest.Frame) ([]byte, error) {
	if f == nil {
		return nil, fmt.Errorf("nil frame")
	}
	vse := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()},
			DataReferenceIndex: 1,
		},
		Width:           uint16(f.Width),
		Height:          uint16(f.Height),
		Horizresolution: 0x00480000, // 72 DPI in 16.16 fixed point
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, vse, mp4.Context{}); err != nil {
		return nil, err
	}
	return withBoxHeader(mp4.BoxTypeAvc1(), payload.Bytes()), nil
}

// MarshalSyncSampleBox encodes keyframe MP4 sample numbers as a full stss box.
func MarshalSyncSampleBox(sampleNumbers []uint32) ([]byte, error) {
	nums := append([]uint32(nil), sampleNumbers...)
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	stss := &mp4.Stss{
		FullBox:      mp4.FullBox{Version: 0},
		EntryCount:   uint32(len(nums)),
		SampleNumber: nums,
	}
	var payload bytes.Buffer
	if _, err := mp4.Marshal(&payload, stss, mp4.Context{}); err != nil {
		return nil, err
	}
	return withBoxHeader(mp4.BoxTypeStss(), payload.Bytes()), nil
}

func withBoxHeader(boxType mp4.BoxType, payload []byte) []byte {
	box := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(box[0:4], uint32(len(box)))
	copy(box[4:8], boxType[:])
	copy(box[8:], payload)
	return box
}

// PackageMP4Metadata builds one sample description and one stss box per track.
func PackageMP4Metadata(frames []*ingest.Frame, idx *Index) (*MP4Metadata, error) {
	if idx == nil {
		return nil, fault.New(fault.StageIndex, "nil_index", -1, -1, fmt.Errorf("index is required"))
	}
	rep := make(map[uint16]*ingest.Frame)
	for _, f := range frames {
		if f != nil && rep[f.TrackID] == nil {
			rep[f.TrackID] = f
		}
	}
	out := &MP4Metadata{Tracks: make(map[uint16]MP4TrackMetadata)}
	for track, f := range rep {
		desc, err := MarshalVisualSampleDescription(f)
		if err != nil {
			return nil, fault.New(fault.StageIndex, "mp4_sample_description", f.Offset, int(track), err)
		}
		var nums []uint32
		for _, k := range idx.Keyframes {
			if k.TrackID == track {
				nums = append(nums, k.SampleNumber)
			}
		}
		stss, err := MarshalSyncSampleBox(nums)
		if err != nil {
			return nil, fault.New(fault.StageIndex, "mp4_stss", f.Offset, int(track), err)
		}
		out.Tracks[track] = MP4TrackMetadata{
			TrackID: track, SampleDescription: desc, SyncSampleBox: stss, SampleNumbers: nums,
		}
	}
	return out, nil
}
