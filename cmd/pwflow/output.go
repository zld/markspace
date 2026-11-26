package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"pwflow/internal/pipeline"
)

func printManifestSummary(m *pipeline.Manifest, manifestSHA string) {
	fmt.Printf("状态: %s\n", m.Status)
	fmt.Printf("片段数: %d\n", m.Counts.Segments)
	fmt.Printf("静音数: %d\n", m.Counts.SilenceIntervals)
	fmt.Printf("重复包数: %d\n", m.Counts.DuplicatePackets)
	fmt.Printf("缺包数: %d\n", m.Counts.MissingPackets)
	fmt.Printf("EBML坏块数: %d\n", m.Counts.EBMLDamages)
	fmt.Printf("解码帧数: %d\n", m.Counts.Frames)
	fmt.Printf("重复内容组: %d\n", m.Counts.DuplicateContent)
	fmt.Printf("亮度均值: %.2f\n", m.LumaMean)
	if m.LoudnessPeakDB != nil {
		fmt.Printf("响度峰值: %.2f dBFS\n", *m.LoudnessPeakDB)
	} else {
		fmt.Println("响度峰值: n/a")
	}
	fmt.Printf("最大对齐误差: %.3f ms (阈值 %.0f ms)\n", m.Alignment.MaxErrorMS, m.Alignment.ThresholdMS)
	if m.BandwidthBpsEWMA > 0 {
		fmt.Printf("吞吐估算: %.0f bps\n", m.BandwidthBpsEWMA)
	}
	if manifestSHA != "" {
		fmt.Printf("清单哈希: %s\n", manifestSHA)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, s := range m.Segments {
		fmt.Fprintf(w, "片段 %d:\t%s\tsha256=%s\tframes=%d\tframes_root=%s\tluma=%.2f\n",
			s.Index, s.File, s.FileSHA256, s.FrameCount, s.FramesSHA256, s.LumaMean)
		for _, f := range s.Frames {
			fmt.Fprintf(w, "  frame %03d:\tpts_ns=%d\tsha256=%s\tpixel=%s\tluma=%.2f\n",
				f.Index, f.PTSNS, f.SHA256, f.PixelSHA256, f.LumaMean)
		}
	}
	w.Flush()
}
