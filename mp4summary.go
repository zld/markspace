package mediakernel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	mp4 "github.com/abema/go-mp4"
)

// Summary is the acceptance summary embedded in the MP4 metadata box.
type Summary struct {
	InputSHA256  string `json:"inputSha256"`
	Tracks       int    `json:"tracks"`
	Segments     int    `json:"segments"`
	SilenceRuns  int    `json:"silenceRuns"`
	Duplicates   int    `json:"duplicates"`
	OutputSHA256 string `json:"outputSha256"`
}

// WriteMP4Summary writes the acceptance summary as JSON inside an MP4 file
// (ftyp + free metadata box) and reads it back to verify the roundtrip.
func WriteMP4Summary(path string, s Summary) error {
	payload, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	ftyp := &mp4.Ftyp{MajorBrand: mp4.BrandISOM()}
	ftyp.AddCompatibleBrand(mp4.BrandISOM())
	if _, err := mp4.Marshal(&buf, ftyp, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(&buf, &mp4.Free{Data: payload}, mp4.Context{}); err != nil {
		return err
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return VerifyMP4Summary(path, payload)
}

// VerifyMP4Summary reads the free box back and compares it to payload.
func VerifyMP4Summary(path string, payload []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	boxes, err := mp4.ExtractBoxesWithPayload(f, nil, []mp4.BoxPath{{mp4.BoxTypeFree()}})
	if err != nil {
		return fmt.Errorf("mp4 summary extract: %w", err)
	}
	if len(boxes) != 1 {
		return fmt.Errorf("mp4 summary: expected 1 free box, got %d", len(boxes))
	}
	free, ok := boxes[0].Payload.(*mp4.Free)
	if !ok {
		return fmt.Errorf("mp4 summary: unexpected payload type %T", boxes[0].Payload)
	}
	if !bytes.Equal(free.Data, payload) {
		return fmt.Errorf("mp4 summary: roundtrip mismatch")
	}
	return nil
}
