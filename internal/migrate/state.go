package migrate

import (
	"mediacore/internal/manifest"
)

// migrationState carries the values shared between prepare and finish.
type migrationState struct {
	opts        Options
	report      *Report
	point       *Point
	pointPath   string
	tempPath    string
	targetPath  string
	srcBytes    []byte
	srcDigest   string
	m           *manifest.Manifest
	targetBytes []byte
	startStage  string
}
