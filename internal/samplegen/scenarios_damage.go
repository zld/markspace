package samplegen

import (
	"encoding/binary"

	auditerrors "media-audit/internal/errors"
	"media-audit/internal/media"
	"media-audit/internal/mp4"
	"media-audit/internal/mxf"
	"media-audit/internal/timebase"
)

func truncatedMXFSample() *Sample {
	data := mxf.BuildTrackFile(1, timebase.MXF(25, 1), 3, normalMetadata(), mxfSampleSpecs())
	cut := len(data) - 12
	return &Sample{
		ID: "truncated-mxf", Kind: "truncated", StableKey: key("truncated-mxf"),
		Format: media.FormatMXF, ExpectedDamageOffset: int64(cut), ExpectedCode: auditerrors.CodeTruncated,
		Files: []FileSpec{{Name: "truncated.mxf", Data: append([]byte(nil), data[:cut]...)}},
	}
}

func truncatedMP4Sample() *Sample {
	data := mp4.BuildTrackFile(mp4.TrackRequest{
		ID: 2, Timebase: timebase.MP4(90000), Width: 320, Height: 240,
		Metadata: normalMetadata(), Samples: mp4SampleSpecs(), Duration: 10800,
	})
	cut := len(data) - 12
	return &Sample{
		ID: "truncated-mp4", Kind: "truncated", StableKey: key("truncated-mp4"),
		Format: media.FormatMP4, ExpectedDamageOffset: int64(cut), ExpectedCode: auditerrors.CodeTruncated,
		Files: []FileSpec{{Name: "truncated.mp4", Data: append([]byte(nil), data[:cut]...)}},
	}
}

func checksumMXFSample() *Sample {
	data := mxf.BuildTrackFile(3, timebase.MXF(25, 1), 3, normalMetadata(), mxfSampleSpecs())
	doc := mxf.Parse("", data)
	off := doc.Tracks[0].Samples[1].RTP.PacketOffset + 15
	data[off] ^= 0x01
	return &Sample{
		ID: "checksum-mxf", Kind: "checksum-failure", StableKey: key("checksum-mxf"),
		Format: media.FormatMXF, ExpectedDamageOffset: off, ExpectedCode: auditerrors.CodeChecksumMismatch,
		Files: []FileSpec{{Name: "checksum.mxf", Data: data}},
	}
}

func checksumMP4Sample() *Sample {
	data := mp4.BuildTrackFile(mp4.TrackRequest{
		ID: 3, Timebase: timebase.MP4(90000), Width: 320, Height: 240,
		Metadata: normalMetadata(), Samples: mp4SampleSpecs(), Duration: 10800,
	})
	doc := mp4.Parse("", data)
	off := doc.Tracks[0].Samples[1].RTP.PacketOffset + 15
	data[off] ^= 0x01
	return &Sample{
		ID: "checksum-mp4", Kind: "checksum-failure", StableKey: key("checksum-mp4"),
		Format: media.FormatMP4, ExpectedDamageOffset: off, ExpectedCode: auditerrors.CodeChecksumMismatch,
		Files: []FileSpec{{Name: "checksum.mp4", Data: data}},
	}
}

func invalidKLVSample() *Sample {
	data := mxf.BuildTrackFile(4, timebase.MXF(25, 1), 3, normalMetadata(), mxfSampleSpecs())
	doc := mxf.Parse("", data)
	var off int64
	for _, el := range doc.Elements {
		if el.Type == "AuditSampleTable" {
			off = el.Offset + 16 // BER length first byte follows 16-byte key
		}
	}
	data[off] = 0x89
	return &Sample{
		ID: "invalid-klv-length", Kind: "invalid-length", StableKey: key("invalid-klv-length"),
		Format: media.FormatMXF, ExpectedDamageOffset: off, ExpectedCode: auditerrors.CodeInvalidLength,
		Files: []FileSpec{{Name: "invalid-klv.mxf", Data: data}},
	}
}

func invalidBoxSample() *Sample {
	data := mp4.BuildTrackFile(mp4.TrackRequest{
		ID: 4, Timebase: timebase.MP4(90000), Width: 320, Height: 240,
		Metadata: normalMetadata(), Samples: mp4SampleSpecs(), Duration: 10800,
	})
	doc := mp4.Parse("", data)
	var off int64
	for _, el := range doc.Elements {
		if el.Type == "asmp" {
			off = el.Offset
		}
	}
	binary.BigEndian.PutUint32(data[off:off+4], 4)
	return &Sample{
		ID: "invalid-box-length", Kind: "invalid-length", StableKey: key("invalid-box-length"),
		Format: media.FormatMP4, ExpectedDamageOffset: off, ExpectedCode: auditerrors.CodeInvalidLength,
		Files: []FileSpec{{Name: "invalid-box.mp4", Data: data}},
	}
}

func timestampBoundarySample() *Sample {
	specs := []media.SampleSpec{
		{KeyFrame: true, PTS: 0, Duration: 3600, Sequence: 65534, Timestamp: 0xfffffff0, Payload: []byte("boundary-0")},
		{KeyFrame: false, PTS: 3600, Duration: 3600, DependsOn: 0, Sequence: 65535, Timestamp: 0x00000010, Payload: []byte("boundary-1")},
		{KeyFrame: true, PTS: 7200, Duration: 3600, Sequence: 0, Timestamp: 0x00000008, Payload: []byte("boundary-2")},
	}
	data := mp4.BuildTrackFile(mp4.TrackRequest{
		ID: 5, Timebase: timebase.MP4(90000), Width: 320, Height: 240,
		Metadata: normalMetadata(), Samples: specs, Duration: 10800,
	})
	doc := mp4.Parse("", data)
	off := doc.Tracks[0].Samples[2].RTP.TimestampOffset
	return &Sample{
		ID: "timestamp-boundary", Kind: "timestamp-boundary", StableKey: key("timestamp-boundary"),
		Format: media.FormatMP4, ExpectedDamageOffset: off, ExpectedCode: auditerrors.CodeTimestampWrap,
		Files: []FileSpec{{Name: "timestamp-boundary.mp4", Data: data}},
	}
}

func invalidTimestampSample() *Sample {
	specs := []media.SampleSpec{
		{KeyFrame: true, PTS: 0, Duration: 1, Sequence: 1, Timestamp: 0, Payload: []byte("ok")},
		{KeyFrame: false, PTS: -1, Duration: 1, DependsOn: 0, Sequence: 2, Timestamp: 3600, Payload: []byte("bad-pts")},
	}
	data := mxf.BuildTrackFile(6, timebase.MXF(25, 1), 2, normalMetadata(), specs)
	doc := mxf.Parse("", data)
	off := doc.Tracks[0].Samples[1].RecordStart + 5 // PTS follows index(4)+flags(1)
	return &Sample{
		ID: "invalid-timestamp", Kind: "invalid-timestamp", StableKey: key("invalid-timestamp"),
		Format: media.FormatMXF, ExpectedDamageOffset: off, ExpectedCode: auditerrors.CodeInvalidTimestamp,
		Files: []FileSpec{{Name: "invalid-timestamp.mxf", Data: data}},
	}
}

func allSamples() []*Sample {
	return []*Sample{
		normalMXFSample(),
		normalMP4Sample(),
		conflictSample(),
		truncatedMXFSample(),
		truncatedMP4Sample(),
		checksumMXFSample(),
		checksumMP4Sample(),
		invalidKLVSample(),
		invalidBoxSample(),
		timestampBoundarySample(),
		invalidTimestampSample(),
	}
}
