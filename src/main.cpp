// Transition Lab — offline cutscene timeline verification tool.
// Pipeline: in-memory generated clips -> timeline + speed map ->
// fixed-point transition kernel -> P6 PPM writer + hash replay ->
// terminal summary.

#include <cstdio>
#include <cstdlib>
#include <string>
#include <vector>

#include "cli.hpp"
#include "frame_buffer.hpp"
#include "ppm_writer.hpp"
#include "report.hpp"
#include "timeline.hpp"
#include "transition.hpp"
#include "verify.hpp"

namespace {

const char kUsage[] =
    "usage: transition_lab demo --out DIR [--fps N] [--frames N]\n"
    "       [--width N] [--height N] [--seed N] [--verify]\n"
    "       [--speed frame=num/den,...] [--wipe ltr|rtl|ttb|btt]\n";

std::string ppm_path(const std::string& dir, const char* type,
                     int index) {
    char name[64];
    std::snprintf(name, sizeof(name), "/%s_%04d.ppm", type, index);
    return dir + name;
}

int run_demo(const tl::DemoConfig& cfg) {
    using namespace tl;

    SpeedMap speed;
    std::string error;
    if (!build_speed_map(cfg, &speed, &error)) {
        print_error("speed map", error);
        return static_cast<int>(ExitCode::Args);
    }
    if (!validate_output_path(cfg.out_dir + "/probe")) {
        print_error("output path", "missing directory or invalid path: " +
                                       cfg.out_dir);
        return static_cast<int>(ExitCode::File);
    }

    // The transition spans the whole timeline so the midpoint frame is
    // inside the blend region; frame 0 is exactly the front clip and the
    // final frame exactly the back clip (endpoint copies in the kernel).
    const int transition_length =
        cfg.transition_length > 0 ? cfg.transition_length : cfg.frames;
    const TransitionType types[3] = {
        TransitionType::Dissolve, TransitionType::Wipe,
        TransitionType::CircleReveal};

    for (TransitionType type : types) {
        TransitionParams params;
        params.direction = cfg.wipe_direction;
        Timeline timeline;
        if (!timeline.build(cfg.width, cfg.height, cfg.frames,
                            cfg.frames - transition_length,
                            transition_length, type, params, speed,
                            cfg.seed, &error)) {
            print_error("timeline", error);
            return static_cast<int>(ExitCode::Args);
        }

        std::vector<std::uint64_t> hashes;
        hashes.reserve(static_cast<std::size_t>(cfg.frames));
        FrameStats mid_stats;
        const int mid = cfg.frames / 2;

        for (int f = 0; f < cfg.frames; ++f) {
            FrameBuffer frame;
            if (!timeline.render_frame(f, &frame, &error)) {
                print_error("render", error);
                return static_cast<int>(ExitCode::Args);
            }
            // Test-only hook: TL_FORCE_MISMATCH=frame corrupts one rendered
            // frame so the replay hash comparison reports a real failure.
            // It never triggers in normal operation.
            if (const char* forced = std::getenv("TL_FORCE_MISMATCH")) {
                if (std::atoi(forced) == f && frame.valid())
                    frame.at(0, 0).r ^= 0xff;
            }
            hashes.push_back(hash_frame(frame));

            if (f == mid) {
                const int src = speed.source_frame(f);
                FrameBuffer reference =
                    make_checkerboard(cfg.width, cfg.height, src, 8,
                                      cfg.seed);
                mid_stats = compute_stats(frame, reference);
            }

            const WriteStatus status =
                write_ppm(ppm_path(cfg.out_dir,
                                   transition_type_name(type), f),
                          frame, &error);
            if (status != WriteStatus::Ok) {
                print_error("write", error);
                return static_cast<int>(ExitCode::File);
            }
        }

        // Deterministic replay pass: re-render every frame and compare
        // hashes, proving cross-process reproducibility of the pipeline.
        ReplayReport replay;
        replay.frames = hashes.size();
        replay.hashes = hashes;
        replay.first_hash = hashes.front();
        replay.last_hash = hashes.back();
        for (int f = 0; f < cfg.frames; ++f) {
            FrameBuffer frame;
            if (!timeline.render_frame(f, &frame, &error)) {
                print_error("replay", error);
                return static_cast<int>(ExitCode::Args);
            }
            if (hash_frame(frame) != hashes[static_cast<std::size_t>(f)]) {
                ++replay.mismatch_frames;
                if (replay.first_mismatch < 0) replay.first_mismatch = f;
            }
        }

        TransitionSummary summary;
        summary.type = type;
        summary.frames = cfg.frames;
        summary.first_hash = replay.first_hash;
        summary.last_hash = replay.last_hash;
        summary.mid_changed_pixels = mid_stats.changed_pixels;
        summary.mid_mae = mid_stats.mae;
        summary.mismatch_frames = replay.mismatch_frames;
        summary.first_mismatch = replay.first_mismatch;
        print_summary_line(summary);

        if (cfg.verify && replay.mismatch_frames != 0) {
            print_error("verify",
                        "hash mismatch at frame " +
                            std::to_string(replay.first_mismatch));
            return static_cast<int>(ExitCode::Verify);
        }
    }
    return static_cast<int>(ExitCode::Ok);
}

}  // namespace

int main(int argc, char** argv) {
    using namespace tl;

    const Options opts = parse_args(argc, argv);
    if (opts.show_help) {
        std::printf("%s", kUsage);
        return static_cast<int>(ExitCode::Ok);
    }
    if (!opts.valid) {
        print_error("arguments", opts.error);
        std::fprintf(stderr, "%s", kUsage);
        return static_cast<int>(ExitCode::Args);
    }
    if (opts.command == "demo") return run_demo(opts.demo);

    print_error("arguments", "unknown command");
    return static_cast<int>(ExitCode::Args);
}
