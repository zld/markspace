package fixture

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
)

// GenerateNormal writes the healthy fixture tree:
//
//	root/audio/<kbps>.rtplog
//	root/video/<kbps>.rtplog
//	root/meta/sample.mp4, sample.mxf
//	root/manifest.json
func GenerateNormal(root string, bitrates []int) error {
	if err := validateBitrates(bitrates); err != nil {
		return err
	}
	for _, kbps := range bitrates {
		if err := WriteRTPLog(trackPath(root, "audio", kbps), AudioSpecs(0, 0), AudioSSRC); err != nil {
			return err
		}
		video := VideoSpecs(false)
		if err := WriteRTPLog(trackPath(root, "video", kbps), video, VideoSSRC); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(MetaDir(root), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(MetaDir(root), "sample.mp4"), sampleMP4(), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(MetaDir(root), "sample.mxf"), sampleMXF(), 0o644); err != nil {
		return err
	}
	return WriteManifest(root, Manifest{Kind: "normal", Bitrates: bitrates})
}

// --- Minimal MP4 encoder (mirrors the test reader's fixture subset) ---

func mp4Box(typ string, payload []byte) []byte {
	var out bytes.Buffer
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(8+len(payload)))
	copy(hdr[4:], typ)
	out.Write(hdr[:])
	out.Write(payload)
	return out.Bytes()
}

func mp4FullBox(typ string, flags uint32, body []byte) []byte {
	payload := make([]byte, 4+len(body))
	payload[3] = byte(flags)
	return mp4Box(typ, payload)
}

func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }

func sampleMP4() []byte {
	identity := [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	var tkhdBody bytes.Buffer
	tkhdBody.Write(make([]byte, 8))
	tkhdBody.Write(be32(1))          // track id
	tkhdBody.Write(be32(0))          // reserved
	tkhdBody.Write(be32(90000))      // duration
	tkhdBody.Write(make([]byte, 16)) // reserved/layer/volume
	for _, m := range identity {
		tkhdBody.Write(be32(uint32(m)))
	}
	tkhdBody.Write(be32(640 << 16))
	tkhdBody.Write(be32(360 << 16))

	var mdhdBody bytes.Buffer
	mdhdBody.Write(make([]byte, 8))
	mdhdBody.Write(be32(90000))
	mdhdBody.Write(be32(90000))
	mdhdBody.Write(make([]byte, 4))

	var hdlrBody bytes.Buffer
	hdlrBody.Write(make([]byte, 4))
	hdlrBody.Write([]byte("vide"))
	hdlrBody.Write(make([]byte, 12))

	var colr bytes.Buffer
	colr.Write([]byte("nclx"))
	colr.Write(be16(9))
	colr.Write(be16(16))
	colr.Write(be16(9))
	colr.Write([]byte{0x80}) // full range

	var mdia bytes.Buffer
	mdia.Write(mp4FullBox("mdhd", 0, mdhdBody.Bytes()))
	mdia.Write(mp4FullBox("hdlr", 0, hdlrBody.Bytes()))
	mdia.Write(mp4Box("colr", colr.Bytes()))

	var trak bytes.Buffer
	trak.Write(mp4FullBox("tkhd", 7, tkhdBody.Bytes()))
	trak.Write(mp4Box("mdia", mdia.Bytes()))

	var out bytes.Buffer
	out.Write(mp4Box("ftyp", append([]byte("isom"), make([]byte, 8)...)))
	out.Write(mp4Box("moov", mp4Box("trak", trak.Bytes())))
	return out.Bytes()
}

// --- Minimal MXF encoder (same KLV subset as ingest/mxf.go) ---

func klvEncode(key [16]byte, val []byte) []byte {
	var out bytes.Buffer
	out.Write(key[:])
	if len(val) < 0x80 {
		out.WriteByte(byte(len(val)))
	} else {
		var body []byte
		for v := len(val); v > 0; v >>= 8 {
			body = append([]byte{byte(v)}, body...)
		}
		out.WriteByte(0x80 | byte(len(body)))
		out.Write(body)
	}
	out.Write(val)
	return out.Bytes()
}

func sampleMXF() []byte {
	var out bytes.Buffer
	out.Write(klvEncode(mxfKey("partition"), []byte{1, 2, 3, 4}))
	out.Write(klvEncode(mxfKey("preface"), []byte("preface")))

	var cdci bytes.Buffer
	cdci.Write(be32(640))
	cdci.Write(be32(360))
	cdci.Write(be16(9))
	cdci.Write(be16(16))
	cdci.Write(be16(9))
	cdci.WriteByte(1)

	var sp bytes.Buffer
	sp.Write(klvEncode(mxfKey("cdci"), cdci.Bytes()))
	sp.Write(klvEncode(mxfKey("rotation"), be32(0)))
	crop := make([]byte, 24)
	for i := 0; i < 6; i++ {
		binary.BigEndian.PutUint32(crop[i*4:], uint32(i+1))
	}
	sp.Write(klvEncode(mxfKey("crop"), crop))
	out.Write(klvEncode(mxfKey("sourcepackage"), sp.Bytes()))
	return out.Bytes()
}

func mxfKey(name string) [16]byte {
	switch name {
	case "partition":
		return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x02, 0x01, 0x10, 0x01, 0x02, 0x01, 0x00}
	case "preface":
		return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01, 0x2f, 0x00}
	case "sourcepackage":
		return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01, 0x24, 0x00}
	case "cdci":
		return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01, 0x47, 0x01}
	case "rotation":
		return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x01, 0x09, 0x01, 0x01, 0x01, 0x01, 0x01, 0x05, 0x00, 0x00, 0x00}
	case "crop":
		return [16]byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x01, 0x09, 0x01, 0x01, 0x01, 0x01, 0x01, 0x06, 0x00, 0x00, 0x00}
	}
	return [16]byte{}
}

// silenceAt helps other fixture builders reason about audio content.
func silenceAt(i int) bool {
	return (i >= 10 && i <= 19) || (i >= 60 && i <= 69)
}
