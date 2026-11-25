package mp4scan

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"

	"github.com/abema/go-mp4"

	apperrors "archivecheck/internal/errors"
)

func boxPath(types ...string) mp4.BoxPath {
	p := make(mp4.BoxPath, len(types))
	for i, typ := range types {
		p[i] = mp4.StrToBoxType(typ)
	}
	return p
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func classifyParseError(err error) *apperrors.Error {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "eof") || strings.Contains(msg, "truncat") || strings.Contains(msg, "unexpected end") {
		return apperrors.Wrap(apperrors.KindTruncated, err)
	}
	return apperrors.Wrap(apperrors.KindContainer, err)
}
