package probe

import (
	"path/filepath"
	"strings"

	"mediapack/internal/model"
)

// Scan dispatches to the MXF or MP4 scanner based on file extension. The
// extension is only a selector: format validity is enforced by the scanner.
func Scan(path string, lim Limits) (*FileMeta, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mxf":
		return ScanMXF(path, lim)
	case ".mp4", ".mov", ".m4v":
		return ScanMP4(path, lim)
	default:
		var es model.Errors
		es.Newf(model.StageProbe, model.CodeUnsupported, path, 0,
			ErrorRange(0, 0), "unsupported container extension: %s", filepath.Ext(path))
		return nil, es.Err()
	}
}
