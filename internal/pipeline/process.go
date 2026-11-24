// Package pipeline connects RTP/TS/demux/mux and transactions for the CLI and
// integration tests.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"medpipe/internal/demux"
	"medpipe/internal/fault"
	"medpipe/internal/mux"
)

// InputDigest returns the stable SHA-256 of input bytes.
func InputDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// MappingDigest hashes explicit mapping rules.
func MappingDigest(rules []demux.MapRule) string {
	b, _ := json.Marshal(rules)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ProcessTSBytes demuxes TS, applies explicit mapping, and writes MP4.
func ProcessTSBytes(data []byte, outPath string, rules []demux.MapRule) (*mux.Result, *demux.Media, []fault.Event, *fault.Error) {
	media, events, derr := demux.DemuxTS(data)
	if media == nil {
		return nil, nil, events, derr
	}
	mappings, merr := demux.SelectTracks(media, rules)
	if merr != nil {
		return nil, media, append(events, fault.Event{Code: merr.Code, Severity: "fatal", Stage: merr.Stage, PID: merr.PID, Message: merr.Message}), merr
	}
	res, werr := mux.WriteMP4(outPath, mappings)
	if werr != nil {
		return nil, media, events, werr
	}
	return res, media, events, derr
}

// ProcessMP4ToMP4 reads an MP4 input, applies mapping, and remuxes it.
func ProcessMP4ToMP4(inputPath, outPath string, rules []demux.MapRule) (*mux.Result, *demux.Media, *fault.Error) {
	media, err := demux.ReadMP4File(inputPath)
	if err != nil {
		return nil, media, err
	}
	mappings, err := demux.SelectTracks(media, rules)
	if err != nil {
		return nil, media, err
	}
	res, err := mux.WriteMP4(outPath, mappings)
	return res, media, err
}
