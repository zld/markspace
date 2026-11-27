package mediacore

// precheckAnomalies inspects RTP captures and returns anomalies together with
// the committed keys that must be aborted.
func precheckAnomalies(sc *Scenario, loader *MemLoader, committed []*Item) ([]RTPAnomaly, []Key) {
	var anomalies []RTPAnomaly
	var abort []Key
	anomalyByURI := map[string][]RTPAnomaly{}
	for uri, raw := range sc.RTP {
		_, an, err := InspectRTPStream("rtp", byteReader(raw), NanosPerSecond/1000)
		if err != nil {
			continue
		}
		if len(an) > 0 {
			anomalyByURI[uri] = an
			anomalies = append(anomalies, an...)
		}
	}
	if len(anomalies) == 0 {
		return nil, nil
	}
	for _, it := range committed {
		if uri := it.Ref.Attrs["RTP"]; uri != "" {
			if _, bad := anomalyByURI[uri]; bad {
				abort = append(abort, Key{it.TrackID, it.Seq})
			}
		}
	}
	return anomalies, abort
}

func keysOf(items []*Item) []Key {
	out := make([]Key, 0, len(items))
	for _, it := range items {
		out = append(out, Key{it.TrackID, it.Seq})
	}
	return out
}

func dedupRollbacks(in []RollbackRecord) []RollbackRecord {
	seen := map[Key]bool{}
	var out []RollbackRecord
	for _, r := range in {
		k := Key{r.TrackID, r.Seq}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}
