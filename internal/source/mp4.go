package source

import (
	"bytes"
	"fmt"
	"os"

	"github.com/abema/go-mp4"

	"mediaprep/internal/model"
)

var supportedMP4Codecs = map[string]bool{
	"avc1": true, // samples are handled as ordered opaque bytes for cut/fingerprint
}

func ParseMP4File(path string) (*MediaFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.NewError(model.CodeLengthOutOfRange, model.PhaseParse, "", -1, "read MP4: %v", err)
	}
	return ParseMP4Bytes(data, path)
}

func ParseMP4Bytes(data []byte, path string) (*MediaFile, error) {
	scanned, err := scanBoxes(data)
	if err != nil {
		return nil, err
	}

	r := bytes.NewReader(data)
	info, err := mp4.Probe(r)
	if err != nil {
		return nil, model.NewError(model.CodeTruncated, model.PhaseParse, "", -1,
			"parse MP4 sample tables with go-mp4: %v", err)
	}

	tkhdBoxes, err := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd(),
	})
	if err != nil {
		return nil, model.NewError(model.CodeTruncated, model.PhaseParse, "", -1, "read tkhd: %v", err)
	}
	hdlrBoxes, _ := mp4.ExtractBoxWithPayload(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeHdlr(),
	})

	file := &MediaFile{Path: path, Format: "mp4", Size: int64(len(data))}
	for _, f := range scanned.fields {
		if f.Scope == ScopeContainer {
			file.ContainerFields = append(file.ContainerFields, f)
		}
	}

	for i, pt := range info.Tracks {
		label := fmt.Sprintf("mp4:%d", pt.TrackID)
		codec := scanned.codecByTrack[label]
		if codec == "" {
			switch pt.Codec {
			case mp4.CodecAVC1:
				codec = "avc1"
			case mp4.CodecMP4A:
				codec = "mp4a"
			default:
				codec = "unknown"
			}
		}
		if !supportedMP4Codecs[codec] {
			off := scanned.codecOffsetByTrack[label]
			return nil, model.NewError(model.CodeUnsupportedCodec, model.PhaseParse, label, off,
				"MP4 codec %q is outside the supported non-fragmented sample-table subset", codec)
		}

		track := &Track{
			Index: i, ID: int(pt.TrackID), Label: label, Codec: codec,
			Clock:    model.NewClock(1, int64(pt.Timescale)),
			Duration: int64(pt.Duration),
		}
		if i < len(hdlrBoxes) {
			if h, ok := hdlrBoxes[i].Payload.(*mp4.Hdlr); ok {
				switch string(h.HandlerType[:]) {
				case "vide":
					track.Role = "video"
				case "soun":
					track.Role = "audio"
				}
			}
		}
		if track.Role == "" {
			track.Role = "video"
		}
		for _, f := range scanned.fields {
			if f.Track == label {
				track.RawFields = append(track.RawFields, f)
			}
		}
		if i < len(tkhdBoxes) {
			if tkhd, ok := tkhdBoxes[i].Payload.(*mp4.Tkhd); ok {
				track.Width = int(tkhd.Width >> 16)
				track.Height = int(tkhd.Height >> 16)
			}
		}
		if pt.AVC != nil && track.Width == 0 {
			track.Width = int(pt.AVC.Width)
			track.Height = int(pt.AVC.Height)
		}

		if err := fillMP4Samples(data, pt, track); err != nil {
			return nil, err
		}
		file.Tracks = append(file.Tracks, track)
	}
	// Container descriptions apply to every track and are the lowest-priority
	// provenance tier during metadata resolution.
	for _, tr := range file.Tracks {
		tr.RawFields = append(tr.RawFields, file.ContainerFields...)
	}
	return file, nil
}

func fillMP4Samples(data []byte, pt *mp4.Track, track *Track) error {
	sampleIndex := 0
	dts := int64(0)
	for _, chunk := range pt.Chunks {
		offset := int64(chunk.DataOffset)
		for j := uint32(0); j < chunk.SamplesPerChunk; j++ {
			if sampleIndex >= len(pt.Samples) {
				return model.NewError(model.CodeLengthOutOfRange, model.PhaseParse, track.Label, offset,
					"stsc names more samples than stts/stsz")
			}
			ps := pt.Samples[sampleIndex]
			size := int(ps.Size)
			if offset < 0 || offset+int64(size) > int64(len(data)) {
				return model.NewError(model.CodeTruncated, model.PhaseParse, track.Label, offset,
					"sample %d size %d extends past source size %d", sampleIndex, size, len(data))
			}
			payload := make([]byte, size)
			copy(payload, data[offset:offset+int64(size)])
			track.Samples = append(track.Samples, Sample{
				Index: sampleIndex, Offset: offset, Size: size,
				DTS: dts, PTS: dts + int64(ps.CompositionTimeOffset),
				Duration: int64(ps.TimeDelta), KeyFrame: true, Payload: payload,
			})
			dts += int64(ps.TimeDelta)
			offset += int64(size)
			sampleIndex++
		}
	}
	if sampleIndex != len(pt.Samples) {
		return model.NewError(model.CodeLengthOutOfRange, model.PhaseParse, track.Label, -1,
			"sample table contains %d stts/stsz entries but stsc locates %d", len(pt.Samples), sampleIndex)
	}
	return nil
}
