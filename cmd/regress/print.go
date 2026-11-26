package main

import "fmt"

// print emits the required terminal summary: defect code/offset, resume
// position, resumed fragment count, bundle digest and checksum verdict.
func (r *runner) print(rep *report) {
	fmt.Printf("representations=%d\n", rep.reps)
	fmt.Printf("fragments_total=%d resumed=%d repaired=%d gap=%d truncated=%d\n",
		rep.fragmentsTotal, rep.fragmentsResume, rep.repaired, rep.gap, rep.truncated)
	fmt.Printf("index_entries=%d index_gap=%d\n", rep.indexEntries, rep.indexGap)
	fmt.Printf("rtp_wraps=%d rtp_rollbacks=%d\n", rep.rtpWraps, rep.rtpRollbacks)
	fmt.Printf("boundary_mismatch=%d\n", rep.boundaryMismatch)
	if rep.localizeTotal > 0 {
		fmt.Printf("localization_accuracy=%d/%d\n", rep.localizeHit, rep.localizeTotal)
	}
	fmt.Printf("cache_hits=%d cache_misses=%d\n", rep.cacheHits, rep.cacheMisses)
	for _, d := range rep.defects {
		fmt.Printf("defect code=%s phase=%s rep=%s fragment=%s box=%s offset=%d\n",
			d.code, d.phase, d.rep, d.frag, d.box, d.offset)
	}
	// resume_ms is reported per committed item (first item shown as anchor).
	if r != nil && r.cp != nil {
		items := r.cp.SortedItems()
		if len(items) > 0 {
			fmt.Printf("resume_ms=%d\n", items[0].ResumeMs)
		}
		fmt.Printf("checkpoint_items=%d\n", r.cp.Count())
		fmt.Printf("bundle_sha256=%s\n", rep.bundleSHA)
	}
	fmt.Printf("checksum_match=%t\n", rep.checksumMatch)
	if rep.interrupted {
		fmt.Printf("interrupted=true processed=%d\n", r.processed)
	}
}
