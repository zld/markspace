package mp4

import (
	"bytes"
	"io"

	"mediatool/internal/timeline"
)

// StreamHandler receives each parsed fragment without the pipeline ever
// holding more than one fragment's boxes in memory. Implementations must
// fully consume payload (it is invalid after the callback returns).
type StreamHandler func(frag *Fragment, fatal error) error

// StreamFragments parses fragments from r one at time and invokes handle.
// Only the current fragment's moof and mdat bytes are buffered, each
// bounded by MaxFragmentSize, so steady-state memory is independent of
// the total number/size of fragments in the stream. A malformed fragment
// is reported through handle and scanning continues with the next box.
func StreamFragments(r io.Reader, handle StreamHandler) error {
	var moofBytes, mdatBytes []byte
	var moofOffset, mdatOffset int64 = -1, -1
	flush := func() error {
		if moofBytes == nil && mdatBytes == nil {
			return nil
		}
		frag, perr := assembleFragment(moofBytes, moofOffset, mdatBytes, mdatOffset)
		if herr := handle(frag, perr); herr != nil {
			return herr
		}
		moofBytes, mdatBytes = nil, nil
		moofOffset, mdatOffset = -1, -1
		return nil
	}
	err := ScanBoxes(r, 0, MaxFragmentSize, func(box BoxHeader, payload io.Reader) error {
		body, rerr := io.ReadAll(payload)
		if rerr != nil {
			return rerr
		}
		switch box.Type.String() {
		case "moof":
			if moofBytes != nil {
				if ferr := flush(); ferr != nil {
					return ferr
				}
			}
			moofBytes = body
			moofOffset = int64(box.PayloadOffset)
		case "mdat":
			mdatBytes = body
			mdatOffset = int64(box.Offset)
			return flush()
		default:
			// Top-level non-fragment boxes (ftyp, ...) are skipped.
		}
		return nil
	})
	if err != nil {
		// Report the trailing error but keep the handler contract.
		_ = handle(nil, err)
		return err
	}
	return flush()
}

// assembleFragment rebuilds a Fragment from separately buffered moof/mdat
// bytes, reusing the same nested-box parser as ParseFragment.
func assembleFragment(moof []byte, moofOffset int64,
	mdat []byte, mdatOffset int64) (*Fragment, error) {
	frag := &Fragment{Seq: -1}
	if moof == nil {
		frag.Rejected = true
		frag.Errors = append(frag.Errors, timeline.ErrorAt(
			timeline.ClassFieldMissing, moofOffset, "fragment has no moof box"))
		return frag, nil
	}
	if mdat == nil {
		frag.Rejected = true
		frag.Errors = append(frag.Errors, timeline.ErrorAt(
			timeline.ClassFieldMissing, moofOffset, "fragment has no mdat box"))
		return frag, nil
	}
	frag.HasMdat = true
	frag.MdatOffset = uint64(mdatOffset)
	frag.MdatPayload = mdat
	if perr := parseMoof(frag, moof, moofOffset); perr != nil {
		frag.Rejected = true
		frag.Errors = append(frag.Errors, timeline.AsMediaError(perr))
	}
	return frag, nil
}

// StreamFromBytes is a convenience wrapper for tests and callers that
// already hold a complete byte slice but want the streaming callback
// semantics (per-fragment bounded buffering).
func StreamFromBytes(data []byte, handle StreamHandler) error {
	return StreamFragments(bytes.NewReader(data), handle)
}
