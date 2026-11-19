#include <atomic>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <filesystem>
#include <string>
#include <thread>
#include <unistd.h>

extern "C" {
#include <libavutil/log.h>
}

#include "audio_output.hpp"
#include "master_clock.hpp"
#include "media_source.hpp"
#include "metrics.hpp"
#include "video_output.hpp"

namespace {

constexpr int kExitOk = 0;
constexpr int kExitArgs = 2;
constexpr int kExitRuntime = 3;

struct Args {
    double duration_s = 12.0;
    int fps_num = 30000, fps_den = 1001;
    int sample_rate = 48000;
    int audio_underrun_ms = 0;
    int video_delay_ms = 0;
    int duplicate_every = 0;
    int missing_pts_every = 0;
    uint32_t seed = 42;
};

// Parse "30000/1001" or "30". Returns false on malformed input.
bool parse_frac(const char* s, int& num, int& den) {
    char* end = nullptr;
    long n = std::strtol(s, &end, 10);
    if (end == s || n <= 0 || n > 1'000'000) return false;
    if (*end == '\0') { num = static_cast<int>(n); den = 1; return true; }
    if (*end != '/') return false;
    long d = std::strtol(end + 1, &end, 10);
    if (*end != '\0' || d <= 0 || d > 1'000'000) return false;
    num = static_cast<int>(n);
    den = static_cast<int>(d);
    return true;
}

void usage(const char* prog) {
    std::fprintf(stderr,
        "usage: %s [options]\n"
        "  --duration SECONDS          sample length (default 12)\n"
        "  --fps NUM/DEN               video frame rate (default 30000/1001)\n"
        "  --sample-rate HZ            audio sample rate (default 48000)\n"
        "  --audio-underrun-ms MS      inject one audio stall of this length\n"
        "  --video-delay-ms MS         decode-latency spike every 7th frame\n"
        "  --duplicate-every N         duplicate every Nth decoded frame\n"
        "  --missing-pts-every N       blank pts of every Nth frame\n"
        "  --seed N                    deterministic RNG seed\n",
        prog);
}

bool parse_args(int argc, char** argv, Args& out) {
    for (int i = 1; i < argc; ++i) {
        auto need = [&](const char* flag) -> const char* {
            if (i + 1 >= argc) {
                std::fprintf(stderr, "error: %s requires a value\n", flag);
                return nullptr;
            }
            return argv[++i];
        };
        const char* a = argv[i];
        const char* v;
        if (!std::strcmp(a, "--duration")) {
            v = need(a); if (!v) return false;
            out.duration_s = std::atof(v);
            if (out.duration_s <= 0 || out.duration_s > 3600) return false;
        } else if (!std::strcmp(a, "--fps")) {
            v = need(a); if (!v) return false;
            if (!parse_frac(v, out.fps_num, out.fps_den)) return false;
        } else if (!std::strcmp(a, "--sample-rate")) {
            v = need(a); if (!v) return false;
            out.sample_rate = std::atoi(v);
            if (out.sample_rate < 8000 || out.sample_rate > 384000) return false;
        } else if (!std::strcmp(a, "--audio-underrun-ms")) {
            v = need(a); if (!v) return false;
            out.audio_underrun_ms = std::atoi(v);
            if (out.audio_underrun_ms < 0) return false;
        } else if (!std::strcmp(a, "--video-delay-ms")) {
            v = need(a); if (!v) return false;
            out.video_delay_ms = std::atoi(v);
            if (out.video_delay_ms < 0) return false;
        } else if (!std::strcmp(a, "--duplicate-every")) {
            v = need(a); if (!v) return false;
            out.duplicate_every = std::atoi(v);
            if (out.duplicate_every < 0) return false;
        } else if (!std::strcmp(a, "--missing-pts-every")) {
            v = need(a); if (!v) return false;
            out.missing_pts_every = std::atoi(v);
            if (out.missing_pts_every < 0) return false;
        } else if (!std::strcmp(a, "--seed")) {
            v = need(a); if (!v) return false;
            long s = std::strtol(v, nullptr, 10);
            if (s < 0) return false;
            out.seed = static_cast<uint32_t>(s);
        } else if (!std::strcmp(a, "--help") || !std::strcmp(a, "-h")) {
            usage(argv[0]);
            std::exit(kExitOk);
        } else {
            std::fprintf(stderr, "error: unknown argument '%s'\n", a);
            return false;
        }
    }
    return true;
}

}  // namespace

int main(int argc, char** argv) {
    av_log_set_level(AV_LOG_QUIET);  // deterministic, parseable terminal output
    Args args;
    if (!parse_args(argc, argv, args)) {
        usage(argv[0]);
        return kExitArgs;
    }

    // The synthetic sample is the only file this program touches; it lives in
    // the system temp dir and is removed before exit.
    std::error_code ec;
    auto path = std::filesystem::temp_directory_path(ec) /
                ("avsync_sample_" + std::to_string(getpid()) + ".mkv");
    if (ec) {
        std::fprintf(stderr, "error: cannot resolve temp directory\n");
        return kExitArgs;
    }

    avsync::SampleConfig scfg;
    scfg.duration_s = args.duration_s;
    scfg.fps_num = args.fps_num;
    scfg.fps_den = args.fps_den;
    scfg.sample_rate = args.sample_rate;
    scfg.seed = args.seed;
    scfg.path = path.string();

    std::string err;
    if (!avsync::generate_sample(scfg, err)) {
        std::fprintf(stderr, "error: sample generation failed: %s\n",
                     err.c_str());
        std::filesystem::remove(path, ec);
        return kExitRuntime;
    }

    // Small bounded queues are deliberate: real-time playback back-pressures
    // the decoder so frames cannot be enqueued far ahead of the master clock
    // and then all appear "late". Capacities still absorb a single injected
    // decode-latency spike without starving the audio device.
    constexpr size_t kAudioQueueCap = 32;
    constexpr size_t kVideoQueueCap = 24;
    avsync::BoundedQueue<avsync::AudioChunk> audio_q(kAudioQueueCap);
    avsync::BoundedQueue<avsync::VideoFrameMsg> video_q(kVideoQueueCap);
    avsync::MasterClock clock;
    avsync::Metrics metrics;

    avsync::InjectionConfig inj;
    inj.video_delay_ms = args.video_delay_ms;
    inj.duplicate_every = args.duplicate_every;
    inj.missing_pts_every = args.missing_pts_every;
    inj.audio_underrun_ms = args.audio_underrun_ms;

    std::atomic<bool> stop{false};
    avsync::DecodeResult dr;

    const int64_t frame_dur_us =
        static_cast<int64_t>(args.fps_den) * 1'000'000 / args.fps_num;
    avsync::AudioOutput audio(audio_q, clock, metrics,
                              args.audio_underrun_ms);
    avsync::VideoOutput video(video_q, clock, metrics, frame_dur_us);

    // Decoder runs concurrently; bounded queues make it follow real time.
    std::thread decoder([&] {
        dr = avsync::decode_into_queues(scfg.path, inj, audio_q, video_q, &stop);
    });
    audio.start(stop);
    video.start(stop);

    decoder.join();
    // EOF (or failure): close queues so blocked consumers wake, drain and join.
    audio_q.close();
    video_q.close();
    audio.stop();
    video.stop();
    std::filesystem::remove(path, ec);

    if (!dr.ok) {
        std::fprintf(stderr, "error: decode/sync failed: %s\n",
                     dr.error.c_str());
        return kExitRuntime;
    }
    if (!audio.error().empty() || !video.error().empty()) {
        std::fprintf(stderr, "error: output thread failure: %s%s\n",
                     audio.error().c_str(), video.error().c_str());
        return kExitRuntime;
    }

    // Tolerate an extra trailing partial chunk in the sample-count assertion.
    const long long expected_samples =
        static_cast<long long>(args.duration_s * args.sample_rate);
    const long long expected_frames =
        static_cast<long long>(args.duration_s * args.fps_num / args.fps_den);

    std::printf("{\n");
    std::printf("  \"seed\": %u,\n", args.seed);
    std::printf("  \"duration_s\": %.3f,\n", args.duration_s);
    std::printf("  \"fps\": \"%d/%d\",\n", args.fps_num, args.fps_den);
    std::printf("  \"sample_rate\": %d,\n", args.sample_rate);
    std::printf("  \"start_time_us\": %lld,\n",
                static_cast<long long>(dr.start_time_us));
    std::printf("  \"video_frames_decoded\": %lld,\n",
                static_cast<long long>(dr.video_frames));
    std::printf("  \"audio_samples_decoded\": %lld,\n",
                static_cast<long long>(dr.audio_samples));
    std::printf("  \"expected_video_frames\": %lld,\n", expected_frames);
    std::printf("  \"expected_audio_samples\": %lld,\n", expected_samples);
    std::printf("  \"displayed_frames\": %lld,\n",
                static_cast<long long>(metrics.displayed()));
    std::printf("  \"dropped_frames\": %lld,\n",
                static_cast<long long>(metrics.dropped()));
    std::printf("  \"repeated_frames\": %lld,\n",
                static_cast<long long>(metrics.repeated()));
    std::printf("  \"audio_underruns\": %lld,\n",
                static_cast<long long>(metrics.underruns()));
    std::printf("  \"missing_pts\": %lld,\n",
                static_cast<long long>(metrics.missing_pts()));
    std::printf("  \"injected_duplicates\": %lld,\n",
                static_cast<long long>(dr.injected_duplicates));
    std::printf("  \"injected_missing_pts\": %lld,\n",
                static_cast<long long>(dr.injected_missing_pts));
    std::printf("  \"injected_audio_underruns\": %d,\n",
                args.audio_underrun_ms > 0 ? 1 : 0);
    std::printf("  %s,\n",
                avsync::stats_to_json("master_drift_ms", metrics.drift()).c_str());
    std::printf("  %s\n",
                avsync::stats_to_json("av_offset_ms", metrics.av_offset()).c_str());
    std::printf("}\n");
    return kExitOk;
}
