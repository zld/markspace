package container

import (
	"fmt"
	"os"

	"github.com/abema/go-mp4"

	"mediacore/internal/core"
)

func (s *Scan) readAudioFormat(f *os.File, stsd *Box, tr *TrackScan) *core.Error {
	for _, entry := range stsd.Children {
		switch entry.Type {
		case "mp4a", "enca":
			var ase mp4.AudioSampleEntry
			if err := decodeFile(f, entry, &ase); err != nil {
				return core.Wrap(core.CodeCorruptFragment, core.StageScan, true,
					"undecodable audio sample entry", err).WithOffset(entry.Offset).WithTrack(tr.ID, 0)
			}
			tr.IsAudio = tr.Handler == "soun" || tr.Handler == ""
			tr.Format = AudioFormat{
				Codec:      entry.Type,
				Channels:   int(ase.ChannelCount),
				SampleRate: int(ase.GetSampleRateInt()),
				SampleSize: int(ase.SampleSize),
			}
			if entry.Type == "enca" {
				return core.New(core.CodeUnsupported, core.StageScan, false,
					"encrypted audio sample entry is not supported").WithOffset(entry.Offset).WithTrack(tr.ID, 0)
			}
			return nil
		case "ac-3", "ec-3", "Opus", "alac":
			return core.New(core.CodeUnsupported, core.StageScan, false,
				fmt.Sprintf("audio codec %q is not AAC", entry.Type)).WithOffset(entry.Offset).WithTrack(tr.ID, 0)
		}
	}
	if tr.IsAudio {
		return core.New(core.CodeUnsupported, core.StageScan, false,
			"audio track has no AAC sample entry").WithOffset(stsd.Offset).WithTrack(tr.ID, 0)
	}
	return nil
}
