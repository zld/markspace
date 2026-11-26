// Package output writes replay clips, the sample index and a summary. Clips
// contain compressed samples copied byte-for-byte; nothing is decoded or
// transcoded.
package output

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"replayproof/internal/index"
	"replayproof/internal/media"
	"replayproof/internal/parse"
)

// ClipSpec describes one event clip.
type ClipSpec struct {
	EventIndex int
	// Start and End are on the source timeline.
	Start media.Rational
	End   media.Rational
	Video []index.Entry
	Audio []index.Entry
	// Deviation is the reported alignment error for the aligned clip start.
	Deviation media.Rational
	// SourceBytes is the number of distinct source bytes the clip references.
	SourceBytes int64
}

// Clip is a finished, atomically published clip.
type Clip struct {
	Spec     ClipSpec
	Path     string
	Bytes    int64
	Hash     string // SHA-256 of the written file, lowercase hex
	FirstDTS media.Rational
	LastEnd  media.Rational
	// Shift is the amount subtracted from source times to make the clip
	// start at zero.
	Shift media.Rational
}

// sourceTrack bundles the source description with the sliced entries.
type sourceTrack struct {
	track   *parse.Track
	entries []index.Entry
	offsets []int64 // absolute offsets in the new file
}

// WriteClip re-packages the planned samples into a standalone MP4 at path.
// The file is written to a temporary name and renamed only after every box
// and sample has been written and flushed, so a failure never publishes a
// partial clip.
func WriteClip(srcPath, path string, meta *parse.Meta, video, audio *parse.Track, spec ClipSpec) (*Clip, error) {
	if len(spec.Video) == 0 {
		return nil, media.New(media.CodeEventWindow, "clip.write", -1, "clip has no video samples")
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return nil, media.Wrap(err, media.CodeIO, "clip.open", -1)
	}
	defer src.Close()

	tmp := path + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return nil, media.Wrap(err, media.CodeIO, "clip.create", -1)
	}
	ok := false
	defer func() {
		if !ok {
			out.Close()
			os.Remove(tmp)
		}
	}()

	tracks := []*sourceTrack{{track: video, entries: spec.Video}}
	if audio != nil && len(spec.Audio) > 0 {
		tracks = append(tracks, &sourceTrack{track: audio, entries: spec.Audio})
	}
	if err := writeMP4(out, src, meta, tracks, spec); err != nil {
		return nil, err
	}
	if err := out.Sync(); err != nil {
		return nil, media.Wrap(err, media.CodeIO, "clip.sync", -1)
	}
	if err := out.Close(); err != nil {
		return nil, media.Wrap(err, media.CodeIO, "clip.close", -1)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, media.Wrap(err, media.CodeIO, "clip.rename", -1)
	}
	ok = true
	fi, err := os.Stat(path)
	if err != nil {
		return nil, media.Wrap(err, media.CodeIO, "clip.stat", -1)
	}
	hash, err := HashFile(path)
	if err != nil {
		return nil, err
	}
	return &Clip{
		Spec:     spec,
		Path:     path,
		Bytes:    fi.Size(),
		Hash:     hash,
		FirstDTS: spec.Video[0].DTS,
		LastEnd:  spec.Video[len(spec.Video)-1].End,
		Shift:    spec.Video[0].DTS,
	}, nil
}

// HashFile returns the lowercase hex SHA-256 of a file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", media.Wrap(err, media.CodeIO, "hash.open", -1)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", media.Wrap(err, media.CodeIO, "hash.read", -1)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashBytes returns the lowercase hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeMP4 lays out ftyp, mdat and moov. Sample offsets stored in stco are
// absolute in the new file and computed from the mdat payload position.
func writeMP4(w io.WriteSeeker, src io.ReadSeeker, meta *parse.Meta, tracks []*sourceTrack, spec ClipSpec) error {
	ftyp, err := ftypBytes()
	if err != nil {
		return err
	}
	if _, err := w.Write(ftyp); err != nil {
		return media.Wrap(err, media.CodeIO, "clip.ftyp", -1)
	}

	mdatStart := int64(len(ftyp)) + 8
	payloadLen := int64(0)
	for _, ts := range tracks {
		ts.offsets = make([]int64, len(ts.entries))
		for i, e := range ts.entries {
			ts.offsets[i] = mdatStart + payloadLen
			payloadLen += int64(e.Size)
		}
	}
	mdatHdr := mp4.BoxInfo{Size: uint64(8 + payloadLen), HeaderSize: 8, Type: mp4.BoxTypeMdat()}
	if _, err := w.Write(mp4.EncodeBoxInfo(&mdatHdr)); err != nil {
		return media.Wrap(err, media.CodeIO, "clip.mdat", -1)
	}
	for _, ts := range tracks {
		for _, e := range ts.entries {
			if _, err := src.Seek(e.Offset, io.SeekStart); err != nil {
				return media.Wrap(err, media.CodeIO, "clip.seek", e.Offset)
			}
			n, err := io.CopyN(w, src, int64(e.Size))
			if err == nil && n != int64(e.Size) {
				err = fmt.Errorf("short sample copy: got %d want %d", n, e.Size)
			}
			if err != nil {
				return media.Wrap(err, media.CodeTruncatedBox, "clip.copy", e.Offset)
			}
		}
	}

	moovBytes, err := buildMoov(meta, tracks)
	if err != nil {
		return err
	}
	if _, err := w.Write(moovBytes); err != nil {
		return media.Wrap(err, media.CodeIO, "clip.moov", -1)
	}
	return nil
}
