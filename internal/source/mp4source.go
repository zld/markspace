package source

import (
	"errors"
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"mediarepair/internal/media"
)

type SampleInfo struct {
	Index      uint32 `json:"index"`
	Offset     int64  `json:"offset"`
	Size       uint32 `json:"size"`
	TimeDelta  uint32 `json:"time_delta"`
	ChunkIndex int    `json:"chunk_index"`
}

type SampleTable struct {
	Path          string       `json:"path"`
	TrackID       uint32       `json:"track_id"`
	Timescale     uint32       `json:"timescale"`
	Duration      uint64       `json:"duration"`
	MajorBrand    string       `json:"major_brand"`
	FastStart     bool         `json:"fast_start"`
	FileSize      int64        `json:"file_size"`
	SampleOffsets []SampleInfo `json:"sample_offsets"`
}

// ReadSampleTable uses go-mp4 only for sample table/container metadata. It
// never performs network access.
func ReadSampleTable(path string, trackID uint32) (*SampleTable, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, media.E(media.CodeWrite, "mp4.open", err).WithFile(path)
	}
	defer f.Close()
	st, err := readSampleTableSeeker(f, path, trackID)
	if err != nil {
		return nil, err
	}
	if err := ValidateSampleOffsets(st); err != nil {
		return nil, err
	}
	return st, nil
}

func readSampleTableSeeker(r io.ReadSeeker, path string, trackID uint32) (*SampleTable, error) {
	info, err := gomp4.Probe(r)
	if err != nil {
		return nil, media.E(media.CodeMP4Truncated, "mp4.probe", err).WithFile(path)
	}
	if len(info.Tracks) == 0 {
		return nil, media.E(media.CodeMP4Truncated, "mp4.tracks", errors.New("no tracks in mp4")).WithFile(path)
	}
	var track *gomp4.Track
	if trackID != 0 {
		for _, t := range info.Tracks {
			if t.TrackID == trackID {
				track = t
				break
			}
		}
		if track == nil {
			return nil, media.E(media.CodeManifest, "mp4.track", errors.New("track not found")).WithFile(path)
		}
	} else {
		track = info.Tracks[0]
	}

	size, err := r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, media.E(media.CodeWrite, "mp4.seek_end", err).WithFile(path)
	}

	out := &SampleTable{
		Path:       path,
		TrackID:    track.TrackID,
		Timescale:  track.Timescale,
		Duration:   track.Duration,
		MajorBrand: string(info.MajorBrand[:]),
		FastStart:  info.FastStart,
		FileSize:   size,
	}

	sampleIndex := 0
	for chunkIndex, chunk := range track.Chunks {
		cursor := int64(chunk.DataOffset)
		for s := uint32(0); s < chunk.SamplesPerChunk; s++ {
			if sampleIndex >= len(track.Samples) {
				return nil, media.E(media.CodeMP4Truncated, "mp4.stsc", errors.New("stsc references sample beyond stts/stsz")).
					WithFile(path).WithOffset(cursor)
			}
			sample := track.Samples[sampleIndex]
			out.SampleOffsets = append(out.SampleOffsets, SampleInfo{
				Index:      uint32(sampleIndex),
				Offset:     cursor,
				Size:       sample.Size,
				TimeDelta:  sample.TimeDelta,
				ChunkIndex: chunkIndex,
			})
			cursor += int64(sample.Size)
			sampleIndex++
		}
	}
	if sampleIndex != len(track.Samples) {
		return nil, media.E(media.CodeMP4Truncated, "mp4.stsz", errors.New("not all samples were assigned to chunks")).
			WithFile(path).WithOffset(size)
	}
	return out, nil
}

func ValidateSampleOffsets(st *SampleTable) error {
	for _, s := range st.SampleOffsets {
		end := s.Offset + int64(s.Size)
		if s.Offset < 0 || s.Offset >= st.FileSize {
			return media.E(media.CodeOffsetOutOfRange, "mp4.sample_offset", errors.New("sample offset points outside file")).
				WithFile(st.Path).WithFrame(int(s.Index + 1)).WithOffset(s.Offset)
		}
		if end > st.FileSize {
			return media.E(media.CodeMP4Truncated, "mp4.sample_data", errors.New("sample extends past end of file")).
				WithFile(st.Path).WithFrame(int(s.Index + 1)).WithOffset(end)
		}
	}
	return nil
}
