package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"

	"mediapipeline/internal/demux"
	"mediapipeline/internal/fault"
	"mediapipeline/internal/metadata"
	"mediapipeline/internal/probe"
)

// Verification states what a valid output must satisfy.
type Verification struct {
	ExpectTracks  int
	ExpectEvents  int
	PayloadSHA256 string
	Format        string
	// EventOrderSHA256, when set, must equal the fingerprint of the output's
	// own events merged on a canonical timescale.  This is what makes the
	// metadata order and de-duplication result re-checkable from the file.
	EventOrderSHA256 string
}

// Verify is the result of inspecting an output file.
type VerificationResult struct {
	Format           string
	Tracks           int
	Events           int
	PayloadSHA256    string
	EventOrderSHA256 string
	PayloadBytes     int64
	Bytes            int64
}

// VerifyFile parses outputPath with the same demuxer and comparing counts and
// the media payload digest, not just the container's self-description.
func VerifyFile(outputPath string, want Verification) error {
	_, err := InspectFile(outputPath, want)
	return err
}

// InspectFile returns the measured properties and an error when any
// expectation is not met.
func InspectFile(outputPath string, want Verification) (VerificationResult, error) {
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return VerificationResult{}, fault.Wrap(fault.StageValidate, want.Format, fault.OpValidation, -1, "read output", err)
	}
	res := VerificationResult{Bytes: int64(len(data))}
	det, err := probe.Detect(data)
	if err != nil {
		return res, err
	}
	res.Format = string(det.Format)
	// The remux always writes MP4, so TS and RTP inputs are expected to
	// change container format while keeping payload bytes identical.
	if want.Format != "" && want.Format != "mpeg-ts" && want.Format != "rtp" && res.Format != want.Format {
		return res, fault.New(fault.StageValidate, res.Format, fault.OpValidation, 0,
			fmt.Sprintf("output format %s does not match input format %s", res.Format, want.Format))
	}
	media, err := demux.ParseMP4(data, demux.Options{SampleEvents: true, AllowMissingIndex: true})
	if err != nil {
		return res, err
	}
	res.Tracks = len(media.Tracks)
	res.Events = len(media.Events)
	payload := payloadOfOutput(data)
	sum := sha256.Sum256(payload)
	res.PayloadSHA256 = hex.EncodeToString(sum[:])
	res.PayloadBytes = int64(len(payload))
	if want.ExpectTracks != 0 && res.Tracks != want.ExpectTracks {
		return res, fault.New(fault.StageValidate, res.Format, fault.OpValidation, 0,
			fmt.Sprintf("track count %d != %d", res.Tracks, want.ExpectTracks))
	}
	if want.ExpectEvents != 0 && res.Events != want.ExpectEvents {
		return res, fault.New(fault.StageValidate, res.Format, fault.OpValidation, 0,
			fmt.Sprintf("event count %d != %d", res.Events, want.ExpectEvents))
	}
	if want.PayloadSHA256 != "" && res.PayloadSHA256 != want.PayloadSHA256 {
		return res, fault.New(fault.StageValidate, res.Format, fault.OpValidation, 0,
			"payload SHA-256 differs between input and output")
	}
	merged, err := metadata.Merge(media.Events)
	if err != nil {
		return res, fault.Wrap(fault.StageValidate, res.Format, fault.OpEvent, -1, "merge output events", err)
	}
	res.EventOrderSHA256 = merged.Fingerprint()
	if want.EventOrderSHA256 != "" && res.EventOrderSHA256 != want.EventOrderSHA256 {
		return res, fault.New(fault.StageValidate, res.Format, fault.OpValidation, 0,
			"event order or de-duplication differs between input and output")
	}
	if merged.DuplicateCount != 0 {
		return res, fault.New(fault.StageValidate, res.Format, fault.OpEvent, 0,
			fmt.Sprintf("output contains %d duplicate events", merged.DuplicateCount))
	}
	return res, nil
}

// payloadOfOutput returns the concatenation of every mdat payload in file
// order, which is exactly the bytes the remuxer wrote.
func payloadOfOutput(data []byte) []byte {
	var out bytes.Buffer
	pos := 0
	for pos+8 <= len(data) {
		size := int64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		header := int64(8)
		if size == 1 {
			if pos+16 > len(data) {
				break
			}
			size = int64(binary.BigEndian.Uint64(data[pos+8:]))
			header = 16
		} else if size == 0 {
			size = int64(len(data) - pos)
		}
		if size < header || int64(pos)+size > int64(len(data)) {
			break
		}
		if typ == "mdat" {
			out.Write(data[pos+int(header) : pos+int(size)])
		}
		pos += int(size)
	}
	return out.Bytes()
}

// PayloadDigest returns the SHA-256 of the concatenated mdat payloads.
func PayloadDigest(data []byte) string {
	s := sha256.Sum256(payloadOfOutput(data))
	return hex.EncodeToString(s[:])
}

// MediaPayloadDigest hashes every sample payload in deterministic order: the
// order of the tracks in the media model, then sample order within each track.
// The remux writer emits exactly this sequence into mdat, so an unchanged
// digest proves the payload bytes survived the round trip.
func MediaPayloadDigest(media *demux.Media) string {
	h := sha256.New()
	for _, tr := range media.Tracks {
		for _, s := range tr.Samples {
			h.Write(s.Data)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// OutputPayloadDigest hashes the mdat payload of a written MP4.
func OutputPayloadDigest(data []byte) string {
	return PayloadDigest(data)
}
