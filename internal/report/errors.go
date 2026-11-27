package report

import "errors"

// errPlaneMismatch is returned when PSNR is asked to compare planes of
// different length or an empty plane.
var errPlaneMismatch = errors.New("psnr: reference and candidate planes must be non-empty and equal length")
