package fixture

const fixtureDuration = "10.5"

func generateWebM(ffmpeg, out string) error {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30:duration=" + fixtureDuration,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=" + fixtureDuration,
		"-af", "volume='if(between(t,2.10,2.90),0,1)':eval=frame,aformat=sample_fmts=s16:sample_rates=48000:channel_layouts=mono",
		"-c:v", "libvpx-vp9", "-b:v", "300k", "-g", "30", "-keyint_min", "30", "-auto-alt-ref", "0",
		"-c:a", "libopus", "-b:a", "64k", "-frame_duration", "20",
		"-map_metadata", "-1", "-fflags", "+bitexact", "-flags", "+bitexact",
		"-f", "webm", out,
	}
	return runFFmpeg(ffmpeg, args...)
}

func generateMP4(ffmpeg, out string) error {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30:duration=" + fixtureDuration,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=" + fixtureDuration,
		"-af", "volume='if(between(t,2.10,2.90),0,1)':eval=frame,aformat=sample_fmts=s16:sample_rates=48000:channel_layouts=mono",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "28", "-g", "30", "-keyint_min", "30",
		"-c:a", "aac", "-b:a", "64k", "-ar", "48000", "-ac", "1",
		"-map_metadata", "-1", "-fflags", "+bitexact", "-flags", "+bitexact",
		"-movflags", "+faststart", out,
	}
	return runFFmpeg(ffmpeg, args...)
}
