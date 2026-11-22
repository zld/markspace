package container

import (
	mp4 "github.com/abema/go-mp4"

	"mq/internal/timeline"
)

// parseStbl 解析样本表容器：stsd（色彩/裁剪）与 stts/stsc/stsz/stco（样本）。
func parseStbl(tm *TrackMeta, payload []byte, base int64) error {
	var stts mp4.Stts
	var stsc mp4.Stsc
	var stsz mp4.Stsz
	var stco mp4.Stco
	var haveSTTS, haveSTSZ, haveSTCO bool
	err := walkBoxes(payload, base, "", func(h BoxHeader, body []byte) error {
		switch h.Type {
		case "stsd":
			if err := parseStsd(tm, body, h.Payload); err != nil {
				return err
			}
		case "stts":
			if err := unmarshalBox(body, &stts); err != nil {
				return timeline.NewErr(timeline.StageParse, "", h.Offset, "bad stts: %v", err)
			}
			haveSTTS = true
		case "stsc":
			if err := unmarshalBox(body, &stsc); err != nil {
				return timeline.NewErr(timeline.StageParse, "", h.Offset, "bad stsc: %v", err)
			}
		case "stsz":
			if err := unmarshalBox(body, &stsz); err != nil {
				return timeline.NewErr(timeline.StageParse, "", h.Offset, "bad stsz: %v", err)
			}
			haveSTSZ = true
		case "stco":
			if err := unmarshalBox(body, &stco); err != nil {
				return timeline.NewErr(timeline.StageParse, "", h.Offset, "bad stco: %v", err)
			}
			haveSTCO = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !haveSTTS || !haveSTSZ || !haveSTCO {
		return timeline.NewErr(timeline.StageParse, "", base,
			"missing required sample table boxes (stts=%v stsz=%v stco=%v)",
			haveSTTS, haveSTSZ, haveSTCO)
	}
	return buildSamples(tm, &stts, &stsc, &stsz, &stco)
}

// parseStsd 解析样本描述：avc1/mp4a 内的 colr（色彩）与 clap（裁剪）。
func parseStsd(tm *TrackMeta, payload []byte, base int64) error {
	c := NewCursor(payload)
	if err := c.Skip(8, "", "stsd header"); err != nil { // fullbox + entry_count
		return err
	}
	for !c.Done() {
		h, err := c.ReadBoxHeader("")
		if err != nil {
			return err
		}
		h.Offset += base
		h.Payload += base
		body, _ := c.Read(h.PayloadLen(), "", "sample entry")
		switch h.Type {
		case "avc1", "avc3", "hev1", "mp4v":
			if len(body) < 78 {
				return timeline.NewErr(timeline.StageParse, "", h.Offset,
					"visual sample entry too short: %d", len(body))
			}
			if err := parseEntryChildren(tm, body[78:], h.Payload+78); err != nil {
				return err
			}
		case "mp4a", "sowt":
			// 音频样本入口：子集内无附加描述。
		default:
			return timeline.NewErr(timeline.StageParse, "", h.Offset,
				"unknown required sample entry %q", h.Type)
		}
	}
	return nil
}

// parseEntryChildren 遍历样本入口内的子盒（colr/clap 等）。
func parseEntryChildren(tm *TrackMeta, payload []byte, base int64) error {
	return walkBoxes(payload, base, "", func(h BoxHeader, body []byte) error {
		switch h.Type {
		case "colr":
			var colr mp4.Colr
			if err := unmarshalBox(body, &colr); err != nil {
				return timeline.NewErr(timeline.StageParse, "", h.Offset, "bad colr: %v", err)
			}
			if string(colr.ColourType[:]) == "nclx" {
				tm.Color = ColorInfo{
					Primaries: colr.ColourPrimaries,
					Transfer:  colr.TransferCharacteristics,
					Matrix:    colr.MatrixCoefficients,
					FullRange: colr.FullRangeFlag,
					Source:    "mp4_colr",
				}
				tm.Offsets["color"] = h.Offset
			}
		case "clap":
			var clap Clap
			if err := unmarshalBox(body, &clap); err != nil {
				return timeline.NewErr(timeline.StageParse, "", h.Offset, "bad clap: %v", err)
			}
			tm.Crop = &CropRect{
				X: ratioToInt(clap.HorizOffN, clap.HorizOffD),
				Y: ratioToInt(clap.VertOffN, clap.VertOffD),
				W: ratioToInt(clap.CleanApertureWidthN, clap.CleanApertureWidthD),
				H: ratioToInt(clap.CleanApertureHeightN, clap.CleanApertureHeightD),
			}
			tm.Offsets["crop"] = h.Offset
		}
		return nil
	})
}

func ratioToInt(n, d uint32) int64 {
	if d == 0 {
		return 0
	}
	return int64(n) / int64(d)
}

// buildSamples 由 stts/stsc/stsz/stco 展开逐样本时长、大小与文件偏移。
func buildSamples(tm *TrackMeta, stts *mp4.Stts, stsc *mp4.Stsc, stsz *mp4.Stsz, stco *mp4.Stco) error {
	count := int(stsz.SampleCount)
	sizes := make([]int64, count)
	for i := range sizes {
		if stsz.SampleSize != 0 {
			sizes[i] = int64(stsz.SampleSize)
		} else if i < len(stsz.EntrySize) {
			sizes[i] = int64(stsz.EntrySize[i])
		}
	}
	durs := make([]int64, 0, count)
	for _, e := range stts.Entries {
		for j := uint32(0); j < e.SampleCount; j++ {
			durs = append(durs, int64(e.SampleDelta))
		}
	}
	if len(durs) < count {
		return timeline.NewErr(timeline.StageParse, "", tm.OffsetOf("edit_rate"),
			"stts covers %d samples, stsz declares %d", len(durs), count)
	}
	// 按 stsc 将样本映射到 chunk 偏移。
	offsets := make([]int64, count)
	sample := 0
	chunk := 0
	entries := stsc.Entries
	for i, e := range entries {
		nextFirst := int64(len(stco.ChunkOffset)) + 1
		if i+1 < len(entries) {
			nextFirst = int64(entries[i+1].FirstChunk)
		}
		for ; int64(chunk+1) < nextFirst && chunk < len(stco.ChunkOffset); chunk++ {
			off := int64(stco.ChunkOffset[chunk])
			for s := uint32(0); s < e.SamplesPerChunk && sample < count; s++ {
				offsets[sample] = off
				off += sizes[sample]
				sample++
			}
		}
	}
	if sample < count {
		return timeline.NewErr(timeline.StageParse, "", tm.OffsetOf("edit_rate"),
			"stsc/stco cover %d samples, stsz declares %d", sample, count)
	}
	tm.Samples = make([]SampleMeta, count)
	for i := 0; i < count; i++ {
		tm.Samples[i] = SampleMeta{Index: i, Duration: durs[i], Size: sizes[i], Offset: offsets[i]}
	}
	return nil
}
