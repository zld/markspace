// Command genfixtures emits the self-contained synthetic frame and RTP
// streams used by the acceptance demo into an output directory. It writes:
//
//	frames.bin          length-prefixed valid frame records
//	audio.rtp.bin       length-prefixed RTP audio packets
//	frames-overflow.bin valid frames followed by an overflowing timestamp
//	frames-corrupt.bin  valid frames with one corrupted payload checksum
//
// No external media or network access is required.
package main

import (
	"fmt"
	"os"

	"example.com/continuity/internal/media"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: genfixtures OUT_DIR")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	spec := media.DefaultSpec()

	frames, err := spec.WriteFrameStream()
	must(err)
	must(os.WriteFile(dir+"/frames.bin", frames, 0o644))

	rtp, err := spec.WriteRTPStream()
	must(err)
	must(os.WriteFile(dir+"/audio.rtp.bin", rtp, 0o644))

	overflow, err := media.WriteOverflowFrameStream()
	must(err)
	must(os.WriteFile(dir+"/frames-overflow.bin", overflow, 0o644))

	corrupt := media.CorruptChecksum(frames)
	must(os.WriteFile(dir+"/frames-corrupt.bin", corrupt, 0o644))

	fmt.Println("fixtures written to", dir)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
