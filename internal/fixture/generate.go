package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"archive-lab/internal/media"
)

// Written lists the files a Generate call produced, in a stable order.
type Written struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// ManifestNames are the HLS/DASH manifest fixtures.
var ManifestNames = []string{
	"normal.m3u8", "replace.m3u8", "metadata.m3u8", "truncated.m3u8",
	"malformed.mpd", "normal.mpd", "master.m3u8",
}

// Generate writes every acceptance fixture under dir and returns what it
// wrote. It is deterministic: running it twice yields identical bytes.
func Generate(dir string) ([]Written, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create fixture dir: %w", err)
	}
	var written []Written
	write := func(name string, data []byte) error {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		written = append(written, Written{Path: name, SHA256: sha(data), Bytes: int64(len(data))})
		return nil
	}

	// Media segments in three variants: normal, replaced and metadata-updated.
	for i := 0; i < SegmentCount; i++ {
		name := fmt.Sprintf("normal-%04d.m4s", i)
		data, err := BuildSegment(i, 0)
		if err != nil {
			return nil, err
		}
		if err := write(filepath.ToSlash(filepath.Join("media", "normal", name)), data); err != nil {
			return nil, err
		}
	}
	// The replace fixture reuses the exact same paths, so only the bytes
	// change. Generating it into the same media/normal tree lets the CLI
	// swap content at an unchanged path.
	normalManifest := hlsPlaylist("media/normal", "")
	if err := write("normal.m3u8", normalManifest); err != nil {
		return nil, err
	}
	// Replace and metadata manifests point at separate trees so all three
	// scenarios can coexist; the swap scenario copies replaced bytes over the
	// normal paths (done by the acceptance script via the `swap` command).
	for i := 0; i < SegmentCount; i++ {
		data, err := BuildSegment(i, 1)
		if err != nil {
			return nil, err
		}
		if err := write(fmt.Sprintf("media/replace/normal-%04d.m4s", i), data); err != nil {
			return nil, err
		}
	}
	if err := write("replace.m3u8", hlsPlaylist("media/replace", "")); err != nil {
		return nil, err
	}
	for i := 0; i < SegmentCount; i++ {
		data, err := BuildSegment(i, 2)
		if err != nil {
			return nil, err
		}
		if err := write(fmt.Sprintf("media/metadata/normal-%04d.m4s", i), data); err != nil {
			return nil, err
		}
	}
	if err := write("metadata.m3u8", hlsPlaylist("media/metadata", "#EXT-X-VERSION:4\n")); err != nil {
		return nil, err
	}

	// Truncated segment: a structurally valid prefix cut mid-mdat. The
	// manifest is well formed; the container check must catch it.
	full, err := BuildSegment(0, 0)
	if err != nil {
		return nil, err
	}
	truncated := full[:len(full)/2]
	if err := write(filepath.ToSlash(filepath.Join("media", "truncated", "normal-0000.m4s")), truncated); err != nil {
		return nil, err
	}
	for i := 1; i < SegmentCount; i++ {
		data, err := BuildSegment(i, 0)
		if err != nil {
			return nil, err
		}
		if err := write(fmt.Sprintf("media/truncated/normal-%04d.m4s", i), data); err != nil {
			return nil, err
		}
	}
	if err := write("truncated.m3u8", hlsPlaylist("media/truncated", "")); err != nil {
		return nil, err
	}

	// Damaged manifest: an EXTINF with a non-numeric duration on line 4.
	malformedHLS := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:4\n#EXTINF:3.003,\nmedia/normal/normal-0000.m4s\n#EXTINF:oops,\nmedia/normal/normal-0001.m4s\n#EXT-X-ENDLIST\n"
	if err := write("malformed.m3u8", []byte(malformedHLS)); err != nil {
		return nil, err
	}

	// Damaged MPD: attribute quotes broken, so XML parsing fails with an
	// offset.
	malformedMPD := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT12.012S">
  <Period>
    <AdaptationSet mimeType="video/mp4">
      <SegmentTemplate timescale=zero media="media/normal/normal-$Number$.m4s" initialization="init.mp4">
        <SegmentTimeline><S t="0" d="1080000" r="3"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v" bandwidth="800000"/>
    </AdaptationSet>
  </Period>
</MPD>
`
	if err := write("malformed.mpd", []byte(malformedMPD)); err != nil {
		return nil, err
	}
	if err := write("normal.mpd", validMPD()); err != nil {
		return nil, err
	}
	if err := write("master.m3u8", []byte(`#EXTM3U
#EXT-X-VERSION:4
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Main",LANGUAGE="en",DEFAULT=YES,URI="audio/en.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,AVERAGE-BANDWIDTH=760000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2",AUDIO="aud"
video/720p.m3u8
`)); err != nil {
		return nil, err
	}

	// JSON index of what was written keeps the fixture self-describing.
	meta, err := json.MarshalIndent(written, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "fixtures.json"), meta, 0o644); err != nil {
		return nil, err
	}
	return written, nil
}

func hlsPlaylist(dir, extra string) []byte {
	body := "#EXTM3U\n" + extra + "#EXT-X-TARGETDURATION:4\n#EXT-X-PLAYLIST-TYPE:VOD\n"
	for i := 0; i < SegmentCount; i++ {
		body += "#EXTINF:1.000,\n" + dir + "/" + fmt.Sprintf("normal-%04d.m4s", i) + "\n"
	}
	body += "#EXT-X-ENDLIST\n"
	return []byte(body)
}

func validMPD() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT4S">
  <Period id="p0">
    <AdaptationSet id="v" mimeType="video/mp4" codecs="raw ">
      <SegmentTemplate timescale="90000" media="media/normal/normal-$Number$.m4s" startNumber="0">
        <SegmentTimeline><S t="0" d="90000" r="3"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v" bandwidth="800000" width="64" height="48"/>
    </AdaptationSet>
  </Period>
</MPD>
`)
}

func sha(b []byte) string {
	s := media.NamedHash(b)
	return s
}
