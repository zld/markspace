package fixture

import (
	"encoding/json"
	"os"

	"pwflow/internal/ebml"
	"pwflow/internal/mp4ref"
)

func makeDamagedWebM(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	segPos := indexBytes(b, ebml.EncodeID(ebml.IDSegment))
	if segPos < 0 {
		return os.ErrInvalid
	}
	h, err := ebml.ReadHeader(b, int64(segPos))
	if err != nil {
		return err
	}
	// Rewrite the known Segment length as unknown in the same VINT width.
	// This exercises unknown-length Segment parsing without shifting any
	// original offsets.
	copy(b[segPos+len(ebml.EncodeID(ebml.IDSegment)):], ebml.EncodeUnknownSize(h.SizeWidth))

	// Append an unknown-length Cluster containing a truncated SimpleBlock,
	// followed by a fully formed Cluster whose Timecode confirms resync.
	var extra []byte
	extra = append(extra, ebml.EncodeID(ebml.IDCluster)...)
	extra = append(extra, ebml.EncodeUnknownSize(4)...)
	extra = append(extra, ebml.EncodeID(ebml.IDSimpleBlock)...)
	extra = append(extra, ebml.EncodeSize(1)...)
	extra = append(extra, 0x81) // one-byte payload: cannot contain block header

	confirm := ebml.EncodeID(ebml.IDTimecode)
	confirm = append(confirm, ebml.EncodeSize(2)...)
	confirm = append(confirm, 0x27, 0x10) // 10000 ms
	extra = append(extra, ebml.EncodeID(ebml.IDCluster)...)
	extra = append(extra, ebml.EncodeSize(uint64(len(confirm)))...)
	extra = append(extra, confirm...)

	return os.WriteFile(dst, append(b, extra...), 0o644)
}

func writeMP4Timeline(mp4Path, out string) error {
	summary, err := mp4ref.ProbeFile(mp4Path)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(out, append(b, '\n'), 0o644)
}

func indexBytes(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
