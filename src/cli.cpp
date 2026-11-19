#include "cli.hpp"

#include <algorithm>
#include <cstring>
#include <fstream>
#include <iostream>
#include <stdexcept>
#include <string>

#include "decode_pipeline.hpp"
#include "fixture_generator.hpp"
#include "path_simulator.hpp"
#include "report.hpp"

namespace pwsync {

namespace {

double requirePositive(const std::string& value, const std::string& flag) {
    size_t consumed = 0;
    double parsed = std::stod(value, &consumed);
    if (consumed != value.size() || parsed < 0.0) {
        throw std::invalid_argument("invalid numeric value for " + flag + ": " + value);
    }
    return parsed;
}

bool fileExists(const std::string& path) {
    std::ifstream f(path, std::ios::binary);
    return f.good();
}

}  // namespace

void printUsage(std::ostream& out, const char* program) {
    out << "usage: " << program << " --input FILE [options]\n"
        << "       " << program << " --generate-fixture FILE.mp4\n\n"
        << "options:\n"
        << "  --json                          emit JSON instead of text summary\n"
        << "  --output FILE                   write report to FILE (default stdout)\n"
        << "  --clock audio|monotonic         master clock mode (default audio)\n"
        << "  --late-ms MS                    late frame threshold (default 35)\n"
        << "  --path-a-delay-ms MS            path A processing delay (default 4)\n"
        << "  --path-b-delay-ms MS            path B processing delay (default 17)\n"
        << "  --discontinuity-tolerance-ms MS rollback/jump tolerance (default 250)\n"
        << "  --late-policy drop|repeat       action for late video (default drop)\n"
        << "  --max-duration SECONDS          safety limit (default 3600)\n"
        << "  --max-frames N                  decoded frame safety limit (default 200000)\n"
        << "  -h, --help                      show this help\n";
}

CliOptions parseCli(int argc, char** argv) {
    CliOptions opt;
    for (int i = 1; i < argc; ++i) {
        const std::string arg = argv[i];
        auto next = [&](const std::string& flag) -> std::string {
            if (i + 1 >= argc) {
                throw std::invalid_argument(flag + " requires a value");
            }
            return argv[++i];
        };
        if (arg == "-h" || arg == "--help") {
            printUsage(std::cout, argv[0]);
            std::exit(0);
        } else if (arg == "--generate-fixture") {
            opt.generate_fixture = true;
            opt.fixture_path = next(arg);
        } else if (arg == "--input") {
            opt.input_path = next(arg);
        } else if (arg == "--output") {
            opt.output_path = next(arg);
        } else if (arg == "--json") {
            opt.json = true;
        } else if (arg == "--clock") {
            opt.clock_mode = parseClockMode(next(arg));
        } else if (arg == "--late-ms") {
            opt.late_threshold_ms = requirePositive(next(arg), arg);
        } else if (arg == "--path-a-delay-ms") {
            opt.path_a_delay_ms = requirePositive(next(arg), arg);
        } else if (arg == "--path-b-delay-ms") {
            opt.path_b_delay_ms = requirePositive(next(arg), arg);
        } else if (arg == "--discontinuity-tolerance-ms") {
            opt.discontinuity_tolerance_ms = requirePositive(next(arg), arg);
        } else if (arg == "--late-policy") {
            const std::string v = next(arg);
            if (v == "drop") {
                opt.policy = LatePolicy::Drop;
            } else if (v == "repeat") {
                opt.policy = LatePolicy::Repeat;
            } else {
                throw std::invalid_argument("--late-policy must be 'drop' or 'repeat'");
            }
        } else if (arg == "--max-duration") {
            opt.limits.max_duration_seconds = requirePositive(next(arg), arg);
        } else if (arg == "--max-frames") {
            const std::string v = next(arg);
            size_t consumed = 0;
            unsigned long long n = std::stoull(v, &consumed);
            if (consumed != v.size()) {
                throw std::invalid_argument("invalid frame limit: " + v);
            }
            opt.limits.max_decoded_frames = n;
        } else {
            throw std::invalid_argument("unknown argument: " + arg);
        }
    }
    if (!opt.generate_fixture && opt.input_path.empty()) {
        throw std::invalid_argument("either --input or --generate-fixture is required");
    }
    if (opt.generate_fixture && !opt.input_path.empty()) {
        throw std::invalid_argument("--generate-fixture cannot be combined with --input");
    }
    return opt;
}

ExitCode runPipeline(const CliOptions& options, std::vector<MediaError>& errors) {
    if (options.generate_fixture) {
        FixtureOptions fixture;
        fixture.output_path = options.fixture_path;
        std::string message;
        if (!generateFixture(fixture, message)) {
            errors.push_back({ErrorCategory::WriteFailed, -1, message});
            return ExitCode::MediaError;
        }
        return ExitCode::Ok;
    }
    if (!fileExists(options.input_path)) {
        errors.push_back({ErrorCategory::OpenFailed, -1,
                          "input file not found: " + options.input_path});
        return ExitCode::Usage;
    }

    SyncReport report;
    report.input_file = options.input_path;
    report.clock_mode = options.clock_mode == ClockMode::AudioMaster ? "audio" : "monotonic";
    report.late_threshold_ms = options.late_threshold_ms;
    report.discontinuity_tolerance_ms = options.discontinuity_tolerance_ms;

    PathConfig path_config;
    path_config.clock_mode = options.clock_mode;
    path_config.discontinuity_tolerance_seconds =
        options.discontinuity_tolerance_ms / 1000.0;
    path_config.sync_a.policy = options.policy;
    path_config.sync_b.policy = options.policy;
    path_config.sync_a.late_threshold_seconds = options.late_threshold_ms / 1000.0;
    path_config.sync_b.late_threshold_seconds = options.late_threshold_ms / 1000.0;
    path_config.sync_a.path_delay_seconds = options.path_a_delay_ms / 1000.0;
    path_config.sync_b.path_delay_seconds = options.path_b_delay_ms / 1000.0;
    PathSimulator simulator(path_config);

    DecodeConfig decode_config;
    decode_config.limits = options.limits;
    decode_config.discontinuity_tolerance_seconds =
        options.discontinuity_tolerance_ms / 1000.0;
    decode_config.on_frame = [&](const NormalizedFrame& frame, bool /*flushed*/) {
        simulator.ingest(frame);
    };

    DecodeResult decoded = runDecodePipeline(options.input_path, decode_config);
    errors = decoded.errors;
    report.media_duration_seconds = decoded.duration_seconds;
    report.timestamp_repairs = decoded.timestamp_repairs;
    report.discontinuities = decoded.discontinuities;
    report.decode_failures = decoded.decode_failures;
    report.flushed_frames = decoded.flushed_frames;
    report.errors = decoded.errors;

    // Nominal clock steps come from the actual stream parameters.
    if (decoded.video_frame_rate > 0.0) {
        path_config.video_nominal_step_seconds = 1.0 / decoded.video_frame_rate;
    }
    if (decoded.audio_sample_rate > 0 && decoded.audio_frame_samples > 0) {
        path_config.audio_nominal_step_seconds =
            static_cast<double>(decoded.audio_frame_samples) / decoded.audio_sample_rate;
    }

    bool structural_failure = false;
    for (const auto& e : decoded.errors) {
        if (e.category == ErrorCategory::OpenFailed ||
            e.category == ErrorCategory::MissingStream ||
            e.category == ErrorCategory::LimitExceeded) {
            structural_failure = true;
        }
        if (e.category == ErrorCategory::CorruptPacket) {
            ++report.corrupted_packets;
        }
    }
    report.video_frames_decoded = simulator.ingestedVideo();
    report.audio_frames_decoded = simulator.ingestedAudio();
    report.video_packets = decoded.video_packets;
    report.audio_packets = decoded.audio_packets;
    simulator.finalize(report);

    if (structural_failure) {
        return ExitCode::MediaError;
    }
    const bool written =
        options.json ? writeJsonReportToFile(report, options.output_path)
                     : ([&] {
                           if (options.output_path == "-") {
                               writeTextSummary(report, std::cout);
                               return static_cast<bool>(std::cout);
                           }
                           std::ofstream out(options.output_path,
                                             std::ios::binary | std::ios::trunc);
                           if (!out) {
                               return false;
                           }
                           writeTextSummary(report, out);
                           return static_cast<bool>(out);
                       })();
    if (!written) {
        errors.push_back({ErrorCategory::WriteFailed, -1,
                          "failed to write report to " + options.output_path});
        return ExitCode::ReportError;
    }
    return ExitCode::Ok;
}

}  // namespace pwsync
