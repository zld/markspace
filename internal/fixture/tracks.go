package fixture

import (
	"bytes"
	"encoding/binary"

	"mediacheck/ebml"
)

func writeTracks(out *bytes.Buffer, c Config) error {
	var tracks bytes.Buffer
	tracks.Write(buildVideoTrack(c))
	audio, err := buildAudioTrack(c)
	if err != nil {
		return err
	}
	tracks.Write(audio)
	el, err := ebml.BuildElement(ebml.IDTracks, tracks.Bytes())
	if err != nil {
		return err
	}
	out.Write(el)
	return nil
}

func putTrackUint(t *bytes.Buffer, id ebml.ElementID, v uint64, n int) {
	b := make([]byte, n)
	for i := 0; i < n; i++ {
		b[n-1-i] = byte(v >> uint(8*i))
	}
	el, _ := ebml.BuildElement(id, b)
	t.Write(el)
}

func buildVideoTrack(c Config) []byte {
	var t bytes.Buffer
	putTrackUint(&t, ebml.IDTrackNumber, uint64(VideoTrackNumber), 1)
	putTrackUint(&t, ebml.IDTrackUID, uint64(VideoTrackUID), 4)
	putTrackUint(&t, ebml.IDTrackType, ebml.TrackTypeVideo, 1)
	codec, _ := ebml.BuildElement(ebml.IDCodecID, []byte("V_RAW_RGB32"))
	t.Write(codec)
	putTrackUint(&t, ebml.IDPixelWidth, uint64(c.Width), 2)
	putTrackUint(&t, ebml.IDPixelHeight, uint64(c.Height), 2)
	// Track colour: BT.709 when conflict mode is on.
	raw := uint32(0)
	if c.UnknownColor {
		raw = 0x2A2A // harness-private enumeration, must be retained
	}
	colorEl, _ := ebml.BuildElement(ebml.IDColour, colorPayload(9, 16, 9, 1, raw))
	t.Write(colorEl)
	entry, _ := ebml.BuildElement(ebml.IDTrackEntry, t.Bytes())
	return entry
}

func buildAudioTrack(c Config) ([]byte, error) {
	var t bytes.Buffer
	putTrackUint(&t, ebml.IDTrackNumber, uint64(AudioTrackNumber), 1)
	putTrackUint(&t, ebml.IDTrackUID, uint64(AudioTrackUID), 4)
	putTrackUint(&t, ebml.IDTrackType, ebml.TrackTypeAudio, 1)
	codec, _ := ebml.BuildElement(ebml.IDCodecID, []byte("A_PCM_F32LE"))
	t.Write(codec)
	putTrackUint(&t, ebml.IDSamplingFreq, uint64(c.SampleRate), 4)
	putTrackUint(&t, ebml.IDChannels, uint64(c.Channels), 1)
	putTrackUint(&t, ebml.IDBitDepth, 32, 1)
	if c.BadLayout {
		// Named layout conflicts with the channel count and the map table
		// contains a repeated position, which the resolver must reject.
		name, _ := ebml.BuildElement(ebml.IDChanLayout, []byte("7.1 surround"))
		t.Write(name)
		t.Write(badChannelMap())
	} else {
		name, _ := ebml.BuildElement(ebml.IDChanLayout, []byte("stereo"))
		t.Write(name)
		t.Write(stereoChannelMap())
	}
	return ebml.BuildElement(ebml.IDTrackEntry, t.Bytes())
}

func colorPayload(matrix, transfer, primaries, rng uint32, raw uint32) []byte {
	var b bytes.Buffer
	put := func(id ebml.ElementID, v uint32) {
		buf := []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		el, _ := ebml.BuildElement(id, buf)
		b.Write(el)
	}
	put(ebml.IDColorMatrix, matrix)
	put(ebml.IDColorTransfer, transfer)
	put(ebml.IDColorPrimaries, primaries)
	put(ebml.IDColorRange, rng)
	if raw != 0 {
		put(ebml.IDColorRaw, raw)
	}
	return b.Bytes()
}

func stereoChannelMap() []byte {
	var b bytes.Buffer
	for i, pos := range []uint32{1, 2} { // FL, FR in order
		row := make([]byte, 8)
		binary.BigEndian.PutUint32(row[0:4], pos)
		binary.BigEndian.PutUint32(row[4:8], uint32(i))
		el, _ := ebml.BuildElement(ebml.IDChanMap, row)
		b.Write(el)
	}
	return b.Bytes()
}

func badChannelMap() []byte {
	var b bytes.Buffer
	// Two rows both claim position 1 (front left): duplicate mapping.
	for i := 0; i < 2; i++ {
		row := make([]byte, 8)
		binary.BigEndian.PutUint32(row[0:4], 1)
		binary.BigEndian.PutUint32(row[4:8], uint32(i))
		el, _ := ebml.BuildElement(ebml.IDChanMap, row)
		b.Write(el)
	}
	return b.Bytes()
}
