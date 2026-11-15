// Command stubencoder is the deterministic encoder test double used by the
// offline demo. It is NOT a real video codec: it quantizes the grayscale
// sample frames and RLE-packs them into the TCOUT container, so PSNR
// numbers describe this synthetic round trip only.
//
// Usage:
//
//	stubencoder --in input.vid --out output.enc --target-kbps N \
//	            --duration-sec D [--fail N] [--truncate N]
//
// --fail N      exit with code N after writing nothing (encoder failure drill)
// --truncate N  exit 0 but leave the output N bytes short (truncation drill)
package main

import (
	"flag"
	"fmt"
	"os"

	"transcode/internal/media"
)

func main() {
	in := flag.String("in", "", "input TCVID container")
	out := flag.String("out", "", "output TCOUT container path")
	targetKbps := flag.Float64("target-kbps", 0, "target bitrate in kbps")
	duration := flag.Float64("duration-sec", 0, "segment duration in seconds")
	failCode := flag.Int("fail", 0, "if >0, exit with this code without encoding")
	truncate := flag.Int("truncate", 0, "if >0, trim this many bytes from the output")
	flag.Parse()

	if *in == "" || *out == "" || *targetKbps <= 0 || *duration <= 0 {
		fmt.Fprintln(os.Stderr, "stubencoder: --in, --out, --target-kbps and --duration-sec are required")
		os.Exit(2)
	}
	if *failCode > 0 {
		fmt.Fprintf(os.Stderr, "stubencoder: simulated encoder failure (exit %d)\n", *failCode)
		os.Exit(*failCode)
	}

	v, err := media.ReadVideo(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stubencoder: read input: %v\n", err)
		os.Exit(1)
	}

	// Quantization step derives from the target bitrate: a higher target
	// buys a finer step (better synthetic PSNR), a lower target a coarser
	// one. The mapping is fixed and deterministic.
	quant := 2 + int(12.0/(1.0+*targetKbps/400.0)) // ranges roughly 2..13
	if quant < 2 {
		quant = 2
	}
	enc := &media.Encoded{Width: v.Width, Height: v.Height, FPS: v.FPS,
		Quant: quant, Frames: make([][]byte, len(v.Frames))}
	for i, frame := range v.Frames {
		q := make([]byte, len(frame))
		for p, b := range frame {
			q[p] = byte(int(b) / quant * quant)
		}
		enc.Frames[i] = q
	}

	// Target file size follows directly from the bitrate and duration:
	// kbps * 1000 / 8 bits-per-byte * seconds.
	targetBytes := int(*targetKbps*1000.0/8.0**duration + 0.5)
	if *truncate > 0 {
		targetBytes -= *truncate
		if targetBytes < 0 {
			targetBytes = 0
		}
	}
	if err := media.WriteEncoded(*out, enc, targetBytes); err != nil {
		fmt.Fprintf(os.Stderr, "stubencoder: write output: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("stubencoder: wrote %s quant=%d target_bytes=%d\n", *out, quant, targetBytes)
}
