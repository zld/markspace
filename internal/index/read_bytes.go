package index

import (
	"io"
	"os"

	"example.com/archiveverify/internal/model"
)

// MaxReadSampleBytes bounds one random-access sample read.
const MaxReadSampleBytes = 64 << 20

// ReadSampleBytes reads exactly one indexed sample using its file byte offset.
// It validates lengths and offset+size overflow before touching the file.
func ReadSampleBytes(root string, e Entry) ([]byte, error) {
	if e.Size == 0 {
		return []byte{}, nil
	}
	if e.ByteOffset < 0 || e.Size > MaxReadSampleBytes {
		return nil, &model.MediaError{Code: model.CodeParse, Source: e.SourceID,
			TrackID: e.TrackID, BoxOffset: e.BoxOffset, Err: io.ErrUnexpectedEOF}
	}
	if int64(e.Size) > maxInt64 || e.ByteOffset > maxInt64-int64(e.Size) {
		return nil, &model.MediaError{Code: model.CodeParse, Source: e.SourceID,
			TrackID: e.TrackID, BoxOffset: e.BoxOffset, Err: io.ErrUnexpectedEOF}
	}
	full := resolveInput(root, e.File)
	st, err := os.Stat(full)
	if err != nil {
		return nil, model.NewParseError(e.File, "", e.BoxOffset, e.ByteOffset, err)
	}
	end := e.ByteOffset + int64(e.Size)
	if end > st.Size() {
		return nil, model.NewParseError(e.File, "mdat", e.BoxOffset, st.Size(),
			io.ErrUnexpectedEOF)
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, model.NewParseError(e.File, "", e.BoxOffset, e.ByteOffset, err)
	}
	defer f.Close()
	buf := make([]byte, e.Size)
	if _, err := f.ReadAt(buf, e.ByteOffset); err != nil {
		return nil, model.NewParseError(e.File, "mdat", e.BoxOffset, e.ByteOffset, err)
	}
	return buf, nil
}

const maxInt64 = int64(^uint64(0) >> 1)
