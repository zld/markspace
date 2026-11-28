package media

import (
	"errors"

	"mediad/timeline"
)

func kindFor(h Handler) TrackKind {
	switch h {
	case HandlerVideo:
		return KindVideo
	case HandlerAudio:
		return KindAudio
	case HandlerSubtitle, HandlerText:
		return KindSubtitle
	default:
		return KindOther
	}
}

// locate attaches path and byte offset to an error, preserving an existing
// located issue and filling in only what it lacks.
func locate(path string, off int64, err error) error {
	if err == nil {
		return nil
	}
	var le *timeline.LocatedError
	if errors.As(err, &le) {
		if le.Issue.File == "" {
			le.Issue.File = path
		}
		if le.Issue.ByteOffset == 0 && off > 0 {
			le.Issue.ByteOffset = off
		}
		return le
	}
	return &timeline.LocatedError{
		Issue: timeline.Issue{Severity: timeline.SeverityError, Code: timeline.CodeUnsupported, File: path, ByteOffset: off, Message: err.Error()},
		Err:   err,
	}
}
