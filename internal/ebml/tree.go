package ebml

import "mediacheck/internal/report"

// ParseTree recursively populates Children for known container elements.
func (r *Reader) ParseTree(e *Element) *report.Error {
	if e == nil || !e.Known {
		return report.NewError(report.EBMLIllegalLength, "unknown_length_not_allowed", "", 0, 0,
			"tree element has unknown length")
	}
	if !isContainer(e.ID) {
		return nil
	}
	off := e.PayloadOffset
	for off < e.End {
		c, err := r.ReadElementAt(off, e.End)
		if err != nil {
			return err
		}
		if !c.Known {
			return report.NewError(report.EBMLIllegalLength, "unknown_length_not_allowed", HexID(c.ID), c.Offset, c.SizeOffset,
				"unknown length is not allowed in metadata tree")
		}
		e.Children = append(e.Children, c)
		if isContainer(c.ID) {
			if err := r.ParseTree(c); err != nil {
				return err
			}
		}
		off = c.End
	}
	return nil
}

// FindChildren returns all direct children with id.
func FindChildren(e *Element, id uint32) []*Element {
	if e == nil {
		return nil
	}
	var out []*Element
	for _, c := range e.Children {
		if c.ID == id {
			out = append(out, c)
		}
	}
	return out
}

// FindFirst returns the first direct child with id.
func FindFirst(e *Element, id uint32) *Element {
	for _, c := range FindChildren(e, id) {
		return c
	}
	return nil
}

func isContainer(id uint32) bool {
	switch id {
	case IDEBMLHeader, IDInfo, IDTracks, IDTrackEntry, IDAudio, IDVideo, IDColour,
		IDSeekHead, IDCluster, IDBlockGroup, IDTags, IDChapters, IDAttachments:
		return true
	default:
		return false
	}
}
