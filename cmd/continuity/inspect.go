package main

import (
	"flag"
	"fmt"
	"os"

	"continuity/internal/media"
)

type corruptReport struct {
	File           string `json:"file"`
	Kind           string `json:"kind"`
	EmbeddedOffset int64  `json:"embedded_offset"`
	ActualOffset   int64  `json:"actual_offset"`
	OffsetDelta    int64  `json:"offset_delta"`
	ForgedBlocks   int    `json:"forged_blocks"`
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
}

func runInspect(args []string) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	file := fs.String("file", "", "local EBML/Matroska/WebM file to inspect")
	defectOffset := fs.Int64("expect-offset", -1, "pre-embedded defect offset")
	if err := fs.Parse(args); err != nil {
		return exitGeneric
	}
	if *file == "" {
		fmt.Fprintln(os.Stderr, "inspect requires --file")
		return exitGeneric
	}
	data, err := media.ReadFileLimited(*file, media.DefaultLimits())
	if err != nil {
		return printInputError(err)
	}
	doc, err := media.ParseEBML(data, media.DefaultLimits())
	if err == nil {
		_, err = media.ExtractBlocks(data, doc)
	}
	if err != nil {
		code := printInputError(err)
		if *defectOffset >= 0 {
			actual := structuredOffset(err)
			fmt.Printf("embedded_offset=%d actual_offset=%d offset_delta=%d\n", *defectOffset, actual, actual-*defectOffset)
		}
		return code
	}
	fmt.Printf("inspect_ok file=%s elements=%d blocks=%d\n", *file, len(doc.All), len(doc.Blocks))
	return exitOK
}

func printInputError(err error) int {
	me, ok := media.AsError(err)
	if ok {
		fmt.Printf("input_corrupt stage=%s offset=%d track=%d", me.Stage, me.Offset, me.Track)
		if me.Path != "" {
			fmt.Printf(" path=%s", me.Path)
		}
		if me.Kind != "" {
			fmt.Printf(" kind=%s", me.Kind)
		}
		fmt.Printf("\nerror=%v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "input error: %v\n", err)
	}
	if media.IsInputCorruption(err) || ok {
		return exitInputCorrupt
	}
	return exitGeneric
}

func structuredOffset(err error) int64 {
	if me, ok := media.AsError(err); ok {
		return me.Offset
	}
	return -1
}

func verifyDefectFile(path, file, kind string, embedded int64) corruptReport {
	r := corruptReport{File: file, Kind: kind, EmbeddedOffset: embedded, ActualOffset: -1}
	data, err := media.ReadFileLimited(path, media.DefaultLimits())
	if err != nil {
		r.Error = err.Error()
		return r
	}
	doc, parseErr := media.ParseEBML(data, media.DefaultLimits())
	// Extraction on the partial document is used only to prove that no block
	// at the illegal offset was materialized. The parse error remains fatal.
	var blocks []*media.Block
	if doc != nil {
		blocks, _ = media.ExtractBlocks(data, doc)
	}
	if parseErr == nil {
		if _, err := media.ExtractBlocks(data, doc); err != nil {
			parseErr = err
		}
	}
	if parseErr == nil {
		r.Error = "expected embedded corruption to fail parsing"
		return r
	}
	r.ActualOffset = structuredOffset(parseErr)
	r.OffsetDelta = r.ActualOffset - embedded
	for _, b := range blocks {
		if b.ElementOffset == embedded {
			r.ForgedBlocks++
		}
	}
	r.OK = r.OffsetDelta == 0 && r.ForgedBlocks == 0
	if me, ok := media.AsError(parseErr); ok {
		r.Error = me.Kind
	} else {
		r.Error = parseErr.Error()
	}
	return r
}

func printCorruptReport(r corruptReport) {
	status := "ok"
	if !r.OK {
		status = "mismatch"
	}
	fmt.Printf("corrupt_sample file=%s kind=%s embedded_offset=%d actual_offset=%d offset_delta=%d forged_blocks=%d status=%s\n",
		r.File, r.Kind, r.EmbeddedOffset, r.ActualOffset, r.OffsetDelta, r.ForgedBlocks, status)
}
