package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"

	"mediaregress/internal/checkpoint"
	"mediaregress/internal/media"
)

// VerifyOutcome is the result of checking an existing manifest.
type VerifyOutcome struct {
	OutputSHA256   string `json:"output_sha256"`
	ProvenanceSHA  string `json:"provenance_sha256"`
	StoredSHA      string `json:"stored_sha256"`
	CheckpointSHA  string `json:"checkpoint_sha256,omitempty"`
	TrackCount     int    `json:"tracks"`
	SegmentCount   int    `json:"segments"`
	DuplicateCount int    `json:"duplicates"`
	ErrorCount     int    `json:"errors"`
	Complete       bool   `json:"complete"`
}

// Verify re-reads a manifest, recomputes its document digest and provenance
// seal, and compares both with what was stored. It also checks the checkpoint
// directory's recorded output digest when one is present.
func Verify(opts Options) (*VerifyOutcome, error) {
	if opts.OutputPath == "" {
		return nil, media.NewError(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"verify requires an output manifest")
	}
	man, err := ReadManifest(opts.OutputPath)
	if err != nil {
		return nil, err
	}
	out := &VerifyOutcome{
		StoredSHA:      man.OutputSHA256,
		TrackCount:     len(man.Tracks),
		SegmentCount:   len(man.Segments),
		DuplicateCount: len(man.Duplicates),
		ErrorCount:     len(man.Errors),
		Complete:       man.Complete,
	}
	// Recompute the seal from the document body.
	stored := man.OutputSHA256
	man.OutputSHA256 = ""
	b, merr := json.Marshal(man)
	if merr != nil {
		return nil, media.WrapError(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"re-encode manifest", merr)
	}
	got := string(media.DigestBytes(b))
	out.OutputSHA256 = got
	if stored == "" || stored != got {
		return out, media.Errorf(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"manifest digest %s does not match stored %s", got, stored)
	}
	if man.Provenance == nil {
		return out, media.NewError(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"manifest has no provenance record")
	}
	out.ProvenanceSHA = man.Provenance.OutputSHA256
	if err := man.Provenance.Verify(); err != nil {
		return out, err
	}
	if !man.Provenance.Complete && man.Complete {
		return out, media.NewError(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"incomplete provenance record presented as a complete manifest")
	}
	// The provenance output digest must cover the processed sections, so a
	// document altered and re-sealed at the envelope level is still caught.
	want, err := contentDigest(man)
	if err != nil {
		return out, err
	}
	if man.Provenance.Output.SHA256 != string(want) {
		return out, media.Errorf(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"provenance covers %s, document content hashes to %s", man.Provenance.Output.SHA256, want)
	}
	// The checkpoint, when present, must agree on the output digest.
	if opts.CheckpointDir != "" {
		if st, err := checkpoint.NewStore(opts.CheckpointDir).Load(); err != nil {
			return out, err
		} else if st != nil {
			for _, tr := range st.Tracks {
				if tr.OutputDigest == "" {
					continue
				}
				out.CheckpointSHA = tr.OutputDigest
				if tr.OutputDigest != got {
					return out, media.Errorf(media.ClassOutputMismatch, media.StageCheckpoint, tr.TrackID, -1,
						"checkpoint digest %s does not match manifest %s", tr.OutputDigest, got)
				}
			}
			if st.InputDigest != "" && st.InputDigest != man.Input.SHA256 {
				return out, media.Errorf(media.ClassInputChanged, media.StageCheckpoint, "", 0,
					"checkpoint input digest %s does not match manifest %s", st.InputDigest, man.Input.SHA256)
			}
		}
	}
	// Input must still exist and be unchanged if we can afford to hash it.
	if opts.Input != "" {
		digest, err := hashFile(opts.Input)
		if err != nil {
			return out, err
		}
		if digest != man.Input.SHA256 {
			return out, media.Errorf(media.ClassInputChanged, media.StageProvenance, "", 0,
				"input digest %s does not match manifest %s", digest, man.Input.SHA256)
		}
	}
	return out, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", media.WrapError(media.ClassCorruptInput, media.StageProvenance, "", 0,
			"open input for verification", err)
	}
	defer f.Close()
	d, err := media.NewDigest(f)
	if err != nil {
		return "", media.WrapError(media.ClassCorruptInput, media.StageProvenance, "", 0,
			"digest input for verification", err)
	}
	return string(d), nil
}

// sameFile reports whether two paths resolve to the same file, used by the CLI
// to avoid overwriting the manifest being verified.
func sameFile(a, b string) bool {
	aa, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	bb, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return aa == bb
}

// equalManifests compares two manifests by their sealed digest.
func equalManifests(a, b *Manifest) bool {
	return reflect.DeepEqual(a.OutputSHA256, b.OutputSHA256)
}
