package fixture

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/abema/go-mp4"

	"continuity/internal/model"
)

// bumpContainerRevision rewrites only the ftyp box's MinorVersion and copies
// the remainder of the file (the mdat box) byte-for-byte. The content
// fingerprint therefore stays identical while the container revision changes.
func bumpContainerRevision(path string, data []byte, newRevision uint32) error {
	r := bytes.NewReader(data)
	boxes, err := mp4.ExtractBoxes(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeFtyp()},
		{mp4.BoxTypeMdat()},
	})
	if err != nil {
		return model.WrapError(model.StageIndex, 0, 0, "locating ftyp/mdat", err)
	}
	var ftypInfo, mdatInfo *mp4.BoxInfo
	for _, bi := range boxes {
		switch bi.Type {
		case mp4.BoxTypeFtyp():
			ftypInfo = bi
		case mp4.BoxTypeMdat():
			mdatInfo = bi
		}
	}
	if ftypInfo == nil || mdatInfo == nil {
		return model.NewError(model.StageIndex, 0, 0, "container missing ftyp or mdat")
	}

	// Build the replacement ftyp in a temp file, which natively supports the
	// seeks go-mp4 needs while finalizing box sizes.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ftyp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	w := mp4.NewWriter(tmp)
	if _, err = w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		_ = tmp.Close()
		return err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: newRevision,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
		},
	}
	if _, err = mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = w.EndBox(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	newFtyp, err := os.ReadFile(tmpName)
	if err != nil {
		return err
	}

	// The regenerated ftyp must occupy the same number of bytes so the mdat
	// payload offsets stay valid; otherwise this surgical change is unsafe.
	if uint64(len(newFtyp)) != ftypInfo.Offset+ftypInfo.Size {
		return model.NewError(model.StageIndex, 0, 0, "ftyp size changed; cannot preserve mdat offsets")
	}
	out := make([]byte, 0, len(newFtyp)+len(data)-int(mdatInfo.Offset))
	out = append(out, newFtyp...)
	out = append(out, data[mdatInfo.Offset:]...)
	return model.AtomicWrite(path, out, 0o644)
}
