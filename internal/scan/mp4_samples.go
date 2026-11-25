package scan

import (
	"os"

	"github.com/abema/go-mp4"
)

func appendSamples(_ *os.File, _ *Index, _ string, _ Track, _ *mp4.BoxInfo, _ *mp4.Stsz, _ *mp4.Stts, _ *mp4.Stsc, _ []uint64) error {
	return nil
}
