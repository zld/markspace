package app

import (
	"mediaprep/internal/metadata"
	"mediaprep/internal/source"
)

type InspectReport struct {
	File     *source.MediaFile
	Metadata []*metadata.Resolved
}

// InspectFile parses one local source and resolves its metadata. It gives
// operators a direct way to observe exit code 2 for corrupt inputs.
func InspectFile(path string) (*InspectReport, error) {
	mf, err := source.ParseFile(path)
	if err != nil {
		return nil, err
	}
	rep := &InspectReport{File: mf}
	for _, tr := range mf.Tracks {
		resolved, err := metadata.ResolveTrack(tr)
		if err != nil {
			return nil, err
		}
		rep.Metadata = append(rep.Metadata, resolved)
	}
	return rep, nil
}
