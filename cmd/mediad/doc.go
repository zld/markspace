// Command mediad is the terminal entry point for the media pipeline.
//
// Two subcommands implement the hand-off contract:
//
//	mediad demo   --out DIR [--fresh]
//	mediad verify --dir DIR
//
// demo generates a deterministic camera corpus, runs a continuous batch and
// an interrupted-then-resumed batch, and writes manifest.json. verify
// re-reads the media, recomputes every segment hash, checks cross-stage
// invariants, and replays the saved checkpoint.
//
// Exit status is part of the contract: 0 means every invariant passed, 1 means
// damaged input produced recoverable output, and 2 means indexing or
// verification failed.
package main
