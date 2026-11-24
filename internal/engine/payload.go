package engine

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"io"

	"mediasync/internal/model"
)

// Payloads are stored base64-encoded and flate-compressed. Compression keeps
// the checkpoint log small for long captures while the field stays a plain
// JSON string that the checkpoint digest covers.
func encodePayload(raw []byte) string {
	var buf bytes.Buffer
	zw, err := flate.NewWriter(&buf, flate.BestSpeed)
	if err != nil {
		return base64.StdEncoding.EncodeToString(raw)
	}
	if _, err := zw.Write(raw); err != nil {
		zw.Close()
		return base64.StdEncoding.EncodeToString(raw)
	}
	if err := zw.Close(); err != nil {
		return base64.StdEncoding.EncodeToString(raw)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func decodePayload(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, model.NewError(model.CodeCheckpointCorrupt, model.Loc{}, "payload is not base64").Wrap(err)
	}
	zr := flate.NewReader(bytes.NewReader(raw))
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, 64<<20))
	if err != nil {
		return nil, model.NewError(model.CodeCheckpointCorrupt, model.Loc{}, "payload is not flate data").Wrap(err)
	}
	return out, nil
}
