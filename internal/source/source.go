package source

import (
	"path/filepath"
	"strings"

	"mediaprep/internal/model"
)

func ParseFile(path string) (*MediaFile, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v":
		return ParseMP4File(path)
	case ".mxf":
		return ParseMXFFile(path)
	default:
		return nil, model.NewError(model.CodeUnsupportedCodec, model.PhaseParse, "", -1,
			"unsupported container extension %q", filepath.Ext(path))
	}
}
