package sample

import (
	"bytes"
	"encoding/binary"
	"os"

	"archive/internal/model"
)

// MXFOptions tunes the generated MXF fixture.
type MXFOptions struct {
	VideoSamples   int   // default 12
	AudioSamples   int   // default 18
	VideoPacketLen int   // default 512
	AudioPacketLen int   // default 256
	CorruptAt      int64 // -1 disables
	TruncateAt     int64 // -1 disables
}

var (
	headerPartitionKey = []byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x01,
		0x0D, 0x01, 0x02, 0x01, 0x01, 0x02, 0x02, 0x00}
	footerPartitionKey = []byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x01,
		0x0D, 0x01, 0x02, 0x01, 0x01, 0x02, 0x04, 0x00}
	videoEssenceKey = []byte{0x06, 0x0E, 0x2B, 0x34, 0x01, 0x02, 0x01, 0x01,
		0x0D, 0x01, 0x03, 0x01, 0x15, 0x01, 0x05, 0x01}
	audioEssenceKey = []byte{0x06, 0x0E, 0x2B, 0x34, 0x01, 0x02, 0x01, 0x01,
		0x0D, 0x01, 0x03, 0x01, 0x16, 0x01, 0x04, 0x02}
)

// GenMXF writes a deterministic MXF: header partition pack, interleaved
// essence KLVs, footer partition pack; then applies the requested damage.
func GenMXF(path string, opts MXFOptions) (*Info, error) {
	if opts.VideoSamples == 0 {
		opts.VideoSamples = 12
	}
	if opts.AudioSamples == 0 {
		opts.AudioSamples = 18
	}
	if opts.VideoPacketLen == 0 {
		opts.VideoPacketLen = 512
	}
	if opts.AudioPacketLen == 0 {
		opts.AudioPacketLen = 256
	}
	var merged []mergedSample
	for i := 0; i < opts.VideoSamples; i++ {
		pts := int64(i) * videoDelta // already on the 90kHz RTP clock
		merged = append(merged, mergedSample{1, model.ToMaster(pts, model.Timescale(videoTimescale)),
			model.PackEssence(1, i%4 == 0, pts, fill(1, i, opts.VideoPacketLen-model.EssenceHdrSize))})
	}
	for i := 0; i < opts.AudioSamples; i++ {
		pts := model.FromMaster(model.ToMaster(int64(i)*audioDelta, model.Timescale(audioTimescale)), model.Timescale(videoTimescale))
		merged = append(merged, mergedSample{2, model.ToMaster(pts, model.Timescale(videoTimescale)),
			model.PackEssence(2, false, pts, fill(2, i, opts.AudioPacketLen-model.EssenceHdrSize))})
	}
	sortMerged(merged)

	// Essence KLVs first; partition offsets depend on their total size.
	info := &Info{CorruptAt: opts.CorruptAt, TruncateAt: opts.TruncateAt}
	essenceLen := int64(0)
	for _, m := range merged {
		essenceLen += 16 + int64(len(berLength(int64(len(m.data))))) + int64(len(m.data))
	}
	headerLen := int64(16 + 1 + 88) // key + short-form length + partition pack
	footerPartition := headerLen + essenceLen

	var out bytes.Buffer
	out.Write(klv(headerPartitionKey, partitionPack(0, footerPartition)))
	off := headerLen
	for _, m := range merged {
		key := videoEssenceKey
		if m.track == 2 {
			key = audioEssenceKey
		}
		k := klv(key, m.data)
		info.Samples = append(info.Samples, SampleInfo{Track: m.track,
			Offset: off + 16 + int64(len(berLength(int64(len(m.data))))), Size: int64(len(m.data))})
		out.Write(k)
		off += int64(len(k))
	}
	out.Write(klv(footerPartitionKey, partitionPack(footerPartition, footerPartition)))
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return nil, err
	}
	if err := applyDamage(path, opts.CorruptAt, opts.TruncateAt); err != nil {
		return nil, err
	}
	return info, nil
}

// klv serialises one key-length-value triplet.
func klv(key, value []byte) []byte {
	out := make([]byte, 0, 16+9+len(value))
	out = append(out, key...)
	out = append(out, berLength(int64(len(value)))...)
	out = append(out, value...)
	return out
}

// berLength encodes a BER length: short form below 128, long form otherwise.
func berLength(n int64) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(n))
	i := 0
	for tmp[i] == 0 {
		i++
	}
	out := []byte{0x80 | byte(8-i)}
	return append(out, tmp[i:]...)
}

// partitionPack builds an 88-byte partition pack value.
func partitionPack(thisPartition, footerPartition int64) []byte {
	b := make([]byte, 0, 88)
	put := func(v uint64) { var t [8]byte; binary.BigEndian.PutUint64(t[:], v); b = append(b, t[:]...) }
	b = append(b, 0x00, 0x01, 0x00, 0x03) // major 1, minor 3
	b = append(b, 0x00, 0x00, 0x00, 0x01) // KAG 1
	put(uint64(thisPartition))            // thisPartition
	put(0)                                // previousPartition
	put(uint64(footerPartition))          // footerPartition
	put(0)                                // headerByteCount
	put(0)                                // indexByteCount
	b = append(b, 0, 0, 0, 0)             // indexSID
	put(0)                                // bodyOffset
	b = append(b, 0, 0, 0, 0)             // bodySID
	b = append(b, make([]byte, 16)...)    // operational pattern UL (placeholder)
	b = append(b, make([]byte, 16)...)    // one essence container UL (placeholder)
	return b
}
