package manifest

import "sort"

// adapter converts one manifest version's raw document into the canonical form.
// It is the only place that may know old field names; every canonical field it
// fills records its source path in FieldMap.
type adapter func(m *Manifest, doc map[string]interface{}) error

var adapters = map[int]adapter{
	VersionLegacy: adaptLegacy,
	VersionV2:     adaptV2,
}

// adaptV2 reads the current schema:
//
//	{
//	  "version": 2,
//	  "asset_id": "...", "title": "...", "languages": ["jpn","eng"],
//	  "max_channels": 8, "require_colour": true, "sequence_base": 0,
//	  "audio": {"2": {"sample_rate_hz": {"min_hz":48000,"max_hz":48000},
//	                  "channel_layout": "5.1"}},
//	  "video": {"1": {"colour": {"matrix_coefficients":9,
//	                             "transfer_characteristics":16,
//	                             "primaries":9,"range":1}}},
//	  "tracks": [{"number":1,"type":"video","codec_id":"V_MPEG4/ISO/AVC",
//	              "language":"jpn","name":"main"}]
//	}
func adaptV2(m *Manifest, doc map[string]interface{}) error {
	allowed := copyKeys(knownTopLevel)
	if err := checkKeys(doc, allowed, VersionV2); err != nil {
		return err
	}
	if err := readCommon(m, doc, "asset_id", "title", "languages", "max_channels",
		"require_colour", "sequence_base"); err != nil {
		return err
	}
	if raw, ok := doc["audio"]; ok {
		if err := mapTrackMap(raw, "audio", func(track uint64, path string, body map[string]interface{}) error {
			return readAudioV2(m, track, path, body)
		}); err != nil {
			return err
		}
	}
	if raw, ok := doc["video"]; ok {
		if err := mapTrackMap(raw, "video", func(track uint64, path string, body map[string]interface{}) error {
			return readVideoV2(m, track, path, body)
		}); err != nil {
			return err
		}
	}
	return readTracks(m, doc, "tracks")
}

func readAudioV2(m *Manifest, track uint64, path string, body map[string]interface{}) error {
	for k := range body {
		switch k {
		case "sample_rate_hz", "channel_layout":
		default:
			// Legacy spellings inside a v2 document are unmappable.
			return unknownFieldError(path+"."+k, VersionV2)
		}
	}
	if raw, ok := body["sample_rate_hz"]; ok {
		path := path + ".sample_rate_hz"
		obj, ok := raw.(map[string]interface{})
		if !ok {
			return fieldErr(path, "sample rate must be an object with min_hz/max_hz")
		}
		var rr RateRange
		for k, v := range obj {
			switch k {
			case "min_hz":
				f, err := num(v, path+".min_hz")
				if err != nil {
					return err
				}
				rr.Min = f
			case "max_hz":
				f, err := num(v, path+".max_hz")
				if err != nil {
					return err
				}
				rr.Max = f
			default:
				return unknownFieldError(path+"."+k, VersionV2)
			}
		}
		if rr.Min <= 0 || rr.Max < rr.Min {
			return fieldErr(path, "window [%v, %v] is not a valid positive range", rr.Min, rr.Max)
		}
		m.AudioRates[track] = rr
		m.FieldMap[canonicalRate(track)] = path
	}
	if raw, ok := body["channel_layout"]; ok {
		path := path + ".channel_layout"
		s, err := str(raw, path)
		if err != nil {
			return err
		}
		m.Layouts[track] = ChannelLayout(s)
		m.FieldMap[canonicalLayout(track)] = path
	}
	return nil
}

func readVideoV2(m *Manifest, track uint64, path string, body map[string]interface{}) error {
	for k := range body {
		if k != "colour" && k != "color" {
			return unknownFieldError(path+"."+k, VersionV2)
		}
	}
	raw, ok := body["colour"]
	if !ok {
		raw = body["color"]
	}
	if !ok {
		return nil
	}
	cp := path + ".colour"
	obj, ok := raw.(map[string]interface{})
	if !ok {
		return fieldErr(cp, "colour must be an object")
	}
	var rule ColourRule
	for k, v := range obj {
		var err error
		switch k {
		case "matrix_coefficients":
			rule.MatrixCoefficients, err = uintVal(v, cp+"."+k)
		case "transfer_characteristics", "transfer":
			rule.Transfer, err = uintVal(v, cp+"."+k)
		case "primaries":
			rule.Primaries, err = uintVal(v, cp+"."+k)
		case "range":
			rule.Range, err = uintVal(v, cp+"."+k)
		default:
			return unknownFieldError(cp+"."+k, VersionV2)
		}
		if err != nil {
			return err
		}
	}
	if rule.MatrixCoefficients == 0 || rule.Transfer == 0 || rule.Primaries == 0 {
		return fieldErr(cp, "colour rule must set matrix_coefficients, transfer and primaries")
	}
	m.VideoColour[track] = rule
	m.FieldMap[canonicalColour(track)] = cp
	return nil
}

// adaptLegacy reads schema version 1, whose field names differ from v2:
//
//	"tracks": [{"number":2, "rate_hz": {"min":48000,"max":48000},
//	            "layout":"5.1"}]
//	"video": {"1": {"colour": {"matrix":9,"transfer":16,"primaries":9}}}
func adaptLegacy(m *Manifest, doc map[string]interface{}) error {
	allowed := copyKeys(knownTopLevel)
	if err := checkKeys(doc, allowed, VersionLegacy); err != nil {
		return err
	}
	if err := readCommon(m, doc, "asset_id", "title", "languages", "max_channels",
		"require_colour", "sequence_base"); err != nil {
		return err
	}
	if raw, ok := doc["audio"]; ok {
		if err := mapTrackMap(raw, "audio", func(track uint64, path string, body map[string]interface{}) error {
			return readAudioLegacy(m, track, path, body)
		}); err != nil {
			return err
		}
	}
	if raw, ok := doc["video"]; ok {
		if err := mapTrackMap(raw, "video", func(track uint64, path string, body map[string]interface{}) error {
			return readVideoLegacy(m, track, path, body)
		}); err != nil {
			return err
		}
	}
	// Legacy per-track audio fields live on the track declaration itself.
	tracks, _ := doc["tracks"].([]interface{})
	for i, raw := range tracks {
		body, ok := raw.(map[string]interface{})
		if !ok {
			return fieldErr(indexPath("tracks", i), "track entry is not an object")
		}
		if _, ok := body["rate_hz"]; ok {
			return unknownFieldError(indexPath("tracks", i)+".rate_hz", VersionLegacy)
		}
		if _, ok := body["layout"]; ok {
			return unknownFieldError(indexPath("tracks", i)+".layout", VersionLegacy)
		}
	}
	return readTracks(m, doc, "tracks")
}

func readAudioLegacy(m *Manifest, track uint64, path string, body map[string]interface{}) error {
	for k := range body {
		switch k {
		case "rate_hz", "layout":
		default:
			return unknownFieldError(path+"."+k, VersionLegacy)
		}
	}
	if raw, ok := body["rate_hz"]; ok {
		cp := path + ".rate_hz"
		obj, ok := raw.(map[string]interface{})
		if !ok {
			return fieldErr(cp, "rate_hz must be an object with min/max")
		}
		var rr RateRange
		for k, v := range obj {
			switch k {
			case "min":
				f, err := num(v, cp+".min")
				if err != nil {
					return err
				}
				rr.Min = f
			case "max":
				f, err := num(v, cp+".max")
				if err != nil {
					return err
				}
				rr.Max = f
			default:
				return unknownFieldError(cp+"."+k, VersionLegacy)
			}
		}
		if rr.Min <= 0 || rr.Max < rr.Min {
			return fieldErr(cp, "window [%v, %v] is not a valid positive range", rr.Min, rr.Max)
		}
		m.AudioRates[track] = rr
		m.FieldMap[canonicalRate(track)] = cp
	}
	if raw, ok := body["layout"]; ok {
		cp := path + ".layout"
		s, err := str(raw, cp)
		if err != nil {
			return err
		}
		m.Layouts[track] = ChannelLayout(s)
		m.FieldMap[canonicalLayout(track)] = cp
	}
	return nil
}

func readVideoLegacy(m *Manifest, track uint64, path string, body map[string]interface{}) error {
	for k := range body {
		if k != "colour" && k != "color" {
			return unknownFieldError(path+"."+k, VersionLegacy)
		}
	}
	raw, ok := body["colour"]
	if !ok {
		raw = body["color"]
	}
	if !ok {
		return nil
	}
	cp := path + ".colour"
	obj, ok := raw.(map[string]interface{})
	if !ok {
		return fieldErr(cp, "colour must be an object")
	}
	var rule ColourRule
	for k, v := range obj {
		var err error
		switch k {
		case "matrix":
			rule.MatrixCoefficients, err = uintVal(v, cp+"."+k)
		case "transfer":
			rule.Transfer, err = uintVal(v, cp+"."+k)
		case "primaries":
			rule.Primaries, err = uintVal(v, cp+"."+k)
		case "range":
			rule.Range, err = uintVal(v, cp+"."+k)
		default:
			return unknownFieldError(cp+"."+k, VersionLegacy)
		}
		if err != nil {
			return err
		}
	}
	if rule.MatrixCoefficients == 0 || rule.Transfer == 0 || rule.Primaries == 0 {
		return fieldErr(cp, "colour rule must set matrix, transfer and primaries")
	}
	m.VideoColour[track] = rule
	m.FieldMap[canonicalColour(track)] = cp
	return nil
}

func readTracks(m *Manifest, doc map[string]interface{}, key string) error {
	raw, ok := doc[key]
	if !ok {
		return nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return fieldErr(key, "tracks must be an array")
	}
	seen := map[uint64]bool{}
	for i, entry := range list {
		path := indexPath(key, i)
		body, ok := entry.(map[string]interface{})
		if !ok {
			return fieldErr(path, "track entry is not an object")
		}
		var decl TrackDecl
		for k, v := range body {
			var err error
			switch k {
			case "number":
				decl.Number, err = trackNumber(v, path+".number")
			case "type":
				decl.Type, err = str(v, path+".type")
			case "codec_id":
				decl.CodecID, err = str(v, path+".codec_id")
			case "language":
				decl.Lang, err = str(v, path+".language")
			case "name":
				decl.Name, err = str(v, path+".name")
			default:
				return unknownFieldError(path+"."+k, m.Version)
			}
			if err != nil {
				return err
			}
		}
		if decl.Number == 0 {
			return fieldErr(path, "track declaration has no number")
		}
		if seen[decl.Number] {
			return fieldErr(path, "track %d declared twice", decl.Number)
		}
		seen[decl.Number] = true
		m.Tracks = append(m.Tracks, decl)
		m.FieldMap[canonicalTrack(decl.Number)] = path
	}
	sort.Slice(m.Tracks, func(i, j int) bool { return m.Tracks[i].Number < m.Tracks[j].Number })
	return nil
}

func readCommon(m *Manifest, doc map[string]interface{}, assetID, title, langs, maxCh, reqColour, seqBase string) error {
	if raw, ok := doc[assetID]; ok {
		s, err := str(raw, assetID)
		if err != nil {
			return err
		}
		m.AssetID = s
		m.FieldMap["asset_id"] = assetID
	}
	if raw, ok := doc[title]; ok {
		s, err := str(raw, title)
		if err != nil {
			return err
		}
		m.Title = s
		m.FieldMap["title"] = title
	}
	if raw, ok := doc[langs]; ok {
		list, ok := raw.([]interface{})
		if !ok {
			return fieldErr(langs, "languages must be an array")
		}
		for i, v := range list {
			s, err := str(v, indexPath(langs, i))
			if err != nil {
				return err
			}
			m.Languages = append(m.Languages, s)
		}
		m.FieldMap["languages"] = langs
	}
	if raw, ok := doc[maxCh]; ok {
		n, err := uintVal(raw, maxCh)
		if err != nil {
			return err
		}
		m.MaxChannels = n
		m.FieldMap["max_channels"] = maxCh
	}
	if raw, ok := doc[reqColour]; ok {
		b, ok := raw.(bool)
		if !ok {
			return fieldErr(reqColour, "value is not a boolean")
		}
		m.RequireColour = b
		m.FieldMap["require_colour"] = reqColour
	}
	if raw, ok := doc[seqBase]; ok {
		n, err := uintVal(raw, seqBase)
		if err != nil {
			return err
		}
		if n > 0xFFFF {
			return fieldErr(seqBase, "sequence base %d exceeds 16 bits", n)
		}
		m.SequenceBase = int64(n)
		m.FieldMap["sequence_base"] = seqBase
	}
	return nil
}

// ValidateCrossFields enforces relationships the adapters cannot check per key.
func (m *Manifest) ValidateCrossFields() error {
	tracks := map[uint64]bool{}
	for _, t := range m.Tracks {
		tracks[t.Number] = true
	}
	check := func(kind string, nums map[uint64]bool, canonical func(uint64) string) error {
		keys := make([]uint64, 0, len(nums))
		for n := range nums {
			keys = append(keys, n)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
		for _, n := range keys {
			if len(tracks) > 0 && !tracks[n] {
				return fieldErr(m.FieldMap[canonical(n)],
					"%s rule names track %d, which the manifest does not declare", kind, n)
			}
		}
		return nil
	}
	rateSet := map[uint64]bool{}
	for n := range m.AudioRates {
		rateSet[n] = true
	}
	layoutSet := map[uint64]bool{}
	for n := range m.Layouts {
		layoutSet[n] = true
	}
	colourSet := map[uint64]bool{}
	for n := range m.VideoColour {
		colourSet[n] = true
	}
	if err := check("audio rate", rateSet, canonicalRate); err != nil {
		return err
	}
	if err := check("channel layout", layoutSet, canonicalLayout); err != nil {
		return err
	}
	if err := check("colour", colourSet, canonicalColour); err != nil {
		return err
	}
	return nil
}

// Digest returns the SHA-256 of the raw manifest bytes, used by checkpoints to
// confirm a resume is running against the same manifest version and content.
func (m *Manifest) Digest() string { return digestOf(m.Raw) }

func copyKeys(src map[string]bool) map[string]bool {
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func indexPath(base string, i int) string { return base + "[" + itoa(i) + "]" }

func itoa(v int) string { return fmtInt(int64(v)) }

func fmtInt(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
