// Package ingest turns raw edge-node inputs into typed, offset-annotated
// records.
//
// Supported inputs:
//
//   - SubRip (.srt): optional index line, "HH:MM:SS,mmm" ranges, multi-line
//     bodies, UTF-8 BOM, millisecond or microsecond fractions.
//   - WebVTT (.vtt): optional header, cue identifiers, cue settings, NOTE/
//     STYLE/REGION blocks, "MM:SS.mmm" or "HH:MM:SS.mmm" timings.
//   - MP4 (.mp4/.m4s): box tree, mdhd timescale, stts/ctts/stsz/stsc/stco
//     sample tables expanded into per-sample DTS, PTS, size and file offset.
//   - RTP (.rtp): length-prefixed packets, sequence accounting, 32-bit
//     timestamp extension.
//   - Timecode sidecars (.tc): 24, 25 and 29.97 drop-frame or non-drop labels.
//   - Drift sidecars (.drift): "source_ms,node_ms" observations per node.
//
// Every parser reports damage as a diagnose.Issue carrying the half-open byte
// span of the original input, then continues. A skipped record never consumes
// the following one: an illegal subtitle cue's body is swallowed with it, and
// an impossible MP4 box length resynchronizes on the next plausible header.
//
// All reads are bounded by Limits so a corrupt length field cannot trigger an
// unbounded allocation or an out-of-range read.
package ingest
