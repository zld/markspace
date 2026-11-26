// Command mediavault indexes PCM and RTP assets into a content-addressed store,
// resumes interrupted runs from committed checkpoints and verifies the sealed
// manifest from the terminal.
//
// Exit codes: 0 success, 2 bad input or arguments, 3 manifest verification
// failure. Fixtures that are expected to be rejected during "demo" do not fail
// the command.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/manifest"
	"example.com/mediavault/internal/store"
)

const usage = `mediavault - content-addressed PCM/RTP archiver

Usage:
  mediavault demo   --dir DIR   generate fixtures, ingest them, seal a manifest
  mediavault verify --dir DIR   independently verify a sealed manifest

Exit codes: 0 ok, 2 usage/input error, 3 manifest verification failure.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	switch fault.CodeOf(err) {
	case fault.CodeVerify:
		return 3
	case fault.CodeArgs, fault.CodeIO, fault.CodeManifest:
		return 2
	case "":
		return 2
	default:
		return 2
	}
}

func parseDir(args []string) (string, error) {
	fs := flag.NewFlagSet("mediavault", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", ".tmp/accept", "working directory for fixtures, objects and the manifest")
	if err := fs.Parse(args); err != nil {
		return "", fault.Wrap(fault.CodeArgs, fault.StageReceived, -1, err, "parse flags")
	}
	if fs.NArg() != 0 {
		return "", fault.New(fault.CodeArgs, fault.StageReceived, -1, "unexpected arguments %v", fs.Args())
	}
	if *dir == "" {
		return "", fault.New(fault.CodeArgs, fault.StageReceived, -1, "--dir must not be empty")
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return "", fault.Wrap(fault.CodeArgs, fault.StageReceived, -1, err, "resolve --dir")
	}
	return abs, nil
}

func runVerify(args []string) int {
	dir, err := parseDir(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitCode(err)
	}
	res, err := verifyDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify failed: %v\n", err)
		return exitCode(err)
	}
	printVerify(res)
	if res.Failed > 0 {
		return 3
	}
	return 0
}

func printVerify(res *manifest.Result) {
	fmt.Printf("manifest_digest=%s\n", res.Digest)
	fmt.Printf("verified=%d\n", res.Verified)
	fmt.Printf("failed=%d\n", res.Failed)
	for _, e := range res.Entries {
		fmt.Printf("entry=%s\n", e)
	}
	for _, f := range res.Failures {
		fmt.Printf("failure=%s\n", f)
	}
}

func verifyDir(dir string) (*manifest.Result, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fault.New(fault.CodeArgs, fault.StageReceived, -1, "working directory %s is missing; run demo first", dir)
	}
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	raw, err := st.ReadFile(manifest.FileName)
	if err != nil {
		return nil, fault.Wrap(fault.CodeManifest, fault.StageArchived, -1, err, "read manifest")
	}
	f, err := manifest.Unmarshal(raw)
	if err != nil {
		return nil, err
	}
	v := &manifest.Verifier{Store: st}
	return v.Verify(f)
}
