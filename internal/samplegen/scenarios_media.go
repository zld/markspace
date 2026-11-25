package samplegen

import (
	"media-audit/internal/media"
	"media-audit/internal/mp4"
	"media-audit/internal/mxf"
	"media-audit/internal/timebase"
)

func normalMetadata() media.Metadata {
	return media.Metadata{
		HasRotation:     true,
		RotationDegrees: 90,
		Crop:            &media.Crop{Top: 8, Right: 8, Bottom: 8, Left: 8},
		Color: &media.Color{
			Primaries: 1, Transfer: 1, Matrix: 1,
			PrimariesName: "BT.709", TransferName: "BT.709", MatrixName: "BT.709",
		},
		MetadataVersion: 3,
	}
}

func mxfSampleSpecs() []media.SampleSpec {
	return []media.SampleSpec{
		{KeyFrame: true, PTS: 0, Duration: 1, Sequence: 1, Timestamp: 0, Payload: []byte("mxf-key-00")},
		{KeyFrame: false, PTS: 1, Duration: 1, DependsOn: 0, Sequence: 2, Timestamp: 3600, Payload: []byte("mxf-p-01")},
		{KeyFrame: true, PTS: 2, Duration: 1, Sequence: 3, Timestamp: 7200, Payload: []byte("mxf-key-02")},
	}
}

func mp4SampleSpecs() []media.SampleSpec {
	return []media.SampleSpec{
		{KeyFrame: true, PTS: 0, Duration: 3600, Sequence: 1, Timestamp: 0, Payload: []byte("mp4-key-000")},
		{KeyFrame: false, PTS: 3600, Duration: 3600, DependsOn: 0, Sequence: 2, Timestamp: 3600, Payload: []byte("mp4-p-001")},
		{KeyFrame: true, PTS: 7200, Duration: 3600, Sequence: 3, Timestamp: 7200, Payload: []byte("mp4-key-002")},
	}
}

func normalMXFSample() *Sample {
	data := mxf.BuildTrackFile(1, timebase.MXF(25, 1), 3, normalMetadata(), mxfSampleSpecs())
	return &Sample{
		ID: "normal-mxf", Kind: "normal", StableKey: key("normal-mxf"),
		Format: media.FormatMXF, Normal: true,
		Files: []FileSpec{{Name: "normal.mxf", Data: data}},
	}
}

func normalMP4Sample() *Sample {
	req := mp4.TrackRequest{
		ID: 1, Timebase: timebase.MP4(90000), Width: 320, Height: 240,
		Metadata: normalMetadata(), Samples: mp4SampleSpecs(), Duration: 10800,
	}
	return &Sample{
		ID: "normal-mp4", Kind: "normal", StableKey: key("normal-mp4"),
		Format: media.FormatMP4, Normal: true,
		Files: []FileSpec{{Name: "normal.mp4", Data: mp4.BuildTrackFile(req)}},
	}
}

func conflictSample() *Sample {
	mxfMeta := normalMetadata()
	mxfMeta.MetadataVersion = 11
	mxfMeta.Color = &media.Color{Primaries: 9, Transfer: 9, Matrix: 9, PrimariesName: "BT.2020", TransferName: "BT.2020", MatrixName: "BT.2020"}

	mp4Meta := normalMetadata()
	mp4Meta.MetadataVersion = 10
	mp4Meta.Color = &media.Color{Primaries: 1, Transfer: 1, Matrix: 1}

	mxfData := mxf.BuildTrackFile(7, timebase.MXF(25, 1), 3, mxfMeta, mxfSampleSpecs())
	mp4Data := mp4.BuildTrackFile(mp4.TrackRequest{
		ID: 7, Timebase: timebase.MP4(90000), Width: 320, Height: 240,
		Metadata: mp4Meta, Samples: mp4SampleSpecs(), Duration: 10800,
	})
	return &Sample{
		ID: "metadata-conflict", Kind: "metadata-conflict", StableKey: key("metadata-conflict"),
		Format: media.FormatMixed, Normal: true,
		Files: []FileSpec{
			{Name: "conflict.mxf", Data: mxfData},
			{Name: "conflict.mp4", Data: mp4Data},
		},
	}
}
