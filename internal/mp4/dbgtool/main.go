package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"mediarun/internal/frames"
)

func main() {
	f, _ := os.Open(os.Args[1])
	info, _ := f.Stat()
	sc, err := frames.NewScanner(f, info.Size(), frames.Limits{})
	if err != nil {
		panic(err)
	}
	i := 0
	for {
		rec, err := sc.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			panic(err)
		}
		if rec.Keyframe() {
			fmt.Printf("key idx=%d off=%d pts=%d\n", i, rec.Offset, rec.Meta.PTS)
		}
		i++
	}
	fmt.Println("records", i)
	for _, e := range sc.Faults() {
		fmt.Println("fault", e)
	}
}
