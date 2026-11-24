package sample

import (
	"os"

	"mediapipe/internal/model"
	"mediapipe/internal/stream"
)

// TruncateTail removes n bytes from the framed stream, usually inside the
// final RTP payload, to exercise truncation recovery.
func TruncateTail(path string, n int64) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if n >= st.Size() {
		n = 1
	}
	return os.Truncate(path, st.Size()-n)
}

// CorruptRTPPayloads flips sample bytes in count evenly distributed RTP records
// without repairing the frame CRC, so reader and audio validation both flag it.
func CorruptRTPPayloads(path string, count int) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_, records, readErr := stream.ReadAll(f)
	_ = f.Close()
	if readErr != nil && !model.IsClass(readErr, model.ErrorTruncated) {
		return readErr
	}
	var rtpOffsets []int64
	for _, rec := range records {
		if rec.Header.Type == stream.RecordRTP && rec.RTPPacket != nil && len(rec.Payload) > 32 {
			rtpOffsets = append(rtpOffsets, rec.PayloadOffset)
		}
	}
	if len(rtpOffsets) == 0 {
		return nil
	}
	picked := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		idx := (i + 1) * len(rtpOffsets) / (count + 1)
		if idx >= len(rtpOffsets) {
			idx = len(rtpOffsets) - 1
		}
		picked = append(picked, rtpOffsets[idx]+24) // inside PCM sample region
	}

	out, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	for _, off := range picked {
		b := []byte{0xff}
		if _, err := out.WriteAt(b, off); err != nil {
			return err
		}
	}
	return nil
}

// WriteBadHeader creates an unparseable stream for exit-code-2 acceptance.
func WriteBadHeader(path string) error {
	bad := make([]byte, stream.FileHeaderSize)
	copy(bad[0:8], "NOTMPIPE")
	return os.WriteFile(path, bad, 0o644)
}
