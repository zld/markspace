// Package index locates every frame's RTP packet inside the MP4 container and
// rebuilds that position table whenever the container or metadata revision
// changes. It is the only module that understands the on-disk box layout.
package index

import (
	"os"
	"path/filepath"

	"continuity/internal/fixture"
	"continuity/internal/model"
)

// Position is one frame's byte range inside the mdat box.
type Position struct {
	Number model.FrameNumber `json:"number"`
	Offset uint64            `json:"offset"` // absolute file offset of the RTP packet
	Length uint32            `json:"length"`
}

// Index is the persisted frame position table.
type Index struct {
	ContainerID      string     `json:"container_id"`
	ContainerVersion uint32     `json:"container_version"`
	MetadataVersion  uint32     `json:"metadata_version"`
	Positions        []Position `json:"positions"`
}

// Build parses the MP4 container for frames and fills ContainerBytes,
// Length and Payload on each frame. A truncated final packet is reported as a
// locatable manifest/index error instead of EOF being swallowed.
func Build(root, containerID string, metaVersion uint32, frames []model.Frame) (*Index, error) {
	path, err := model.SafeJoin(root, filepath.Join("input", containerID))
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.WrapError(model.StageIndex, 0, 0, "reading container", err)
	}
	minor, mdat, err := fixture.ContainerHeader(data)
	if err != nil {
		return nil, model.WrapError(model.StageIndex, 0, 0, "parsing MP4 boxes", err)
	}
	if mdat == nil {
		return nil, model.NewError(model.StageIndex, 0, 0, "container has no mdat box")
	}

	idx := &Index{
		ContainerID:      containerID,
		ContainerVersion: minor,
		MetadataVersion:  metaVersion,
		Positions:        make([]Position, 0, len(frames)),
	}
	off := mdat.Offset + mdat.HeaderSize
	for i := range frames {
		num := model.FrameNumber(i + 1)
		remaining := uint64(len(data)) - off
		if remaining == 0 {
			return nil, model.NewError(model.StageManifest, num, int64(off),
				"container truncated: no bytes remain for frame packet")
		}
		// Read the RTP length prefix-free: Unmarshal consumes exactly one packet.
		pkt := &rtpPacket{}
		n, perr := pkt.consume(data[off:])
		if perr != nil {
			return nil, model.NewError(model.StageManifest, num, int64(off),
				"truncated or corrupted RTP packet: "+perr.Error())
		}
		// The RTP packet is header + payload; only the payload length is
		// declared by the manifest, so compare those two directly.
		if uint32(len(pkt.payload)) != frames[i].PayloadLength {
			return nil, model.NewError(model.StageManifest, num, int64(off),
				"rtp payload length does not match declared payload_length")
		}
		idx.Positions = append(idx.Positions, Position{
			Number: num,
			Offset: off,
			Length: uint32(n),
		})
		frames[i].ContainerBytes = off
		frames[i].Payload = append([]byte(nil), pkt.payload...)
		off += uint64(n)
	}
	return idx, nil
}
