#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

#include "access_unit.hpp"
#include "byte_source.hpp"
#include "report.hpp"
#include "sample_generator.hpp"

namespace h264inspect {

namespace {

constexpr int kExitOk = 0;
constexpr int kExitUsage = 1;
constexpr int kExitStreamErrors = 2;

void print_usage(std::FILE* out) {
    std::fprintf(out,
                 "h264-inspect - Annex B H.264 structure analyzer\n"
                 "\n"
                 "Usage:\n"
                 "  h264-inspect analyze <file.h264> [--format text|json]\n"
                 "  h264-inspect --generate-sample <path>\n"
                 "  h264-inspect --generate-interlaced-sample <path>\n"
                 "  h264-inspect --help\n"
                 "\n"
                 "Exit codes:\n"
                 "  0  analysis completed without recoverable stream errors\n"
                 "  1  bad arguments or unreadable input file\n"
                 "  2  analysis completed, but the stream had recoverable errors\n");
}

bool write_file(const std::string& path, const std::vector<std::uint8_t>& bytes) {
    std::FILE* file = std::fopen(path.c_str(), "wb");
    if (file == nullptr) return false;
    bool ok = true;
    if (!bytes.empty()) {
        ok = std::fwrite(bytes.data(), 1, bytes.size(), file) == bytes.size();
    }
    ok = std::fclose(file) == 0 && ok;
    return ok;
}

int parse_format(const std::string& value, ReportFormat& format) {
    if (value == "json") {
        format = ReportFormat::Json;
        return kExitOk;
    }
    if (value == "text") {
        format = ReportFormat::Text;
        return kExitOk;
    }
    std::fprintf(stderr, "error: unknown format '%s' (expected text or json)\n",
                 value.c_str());
    return kExitUsage;
}

int run_generate(const std::string& path, bool interlaced) {
    const std::vector<std::uint8_t> bytes =
        interlaced ? generate_interlaced_sample() : generate_reference_sample();
    if (!write_file(path, bytes)) {
        std::fprintf(stderr, "error: cannot write '%s'\n", path.c_str());
        return kExitUsage;
    }
    std::fprintf(stderr, "wrote %zu bytes to %s\n", bytes.size(), path.c_str());
    return kExitOk;
}

int run_analyze(const std::string& path, ReportFormat format) {
    ByteSource source;
    Diagnostic error;
    if (!source.open(path, ByteSource::kDefaultMaxSize, &error)) {
        std::fprintf(stderr, "error: %s\n", error.message.c_str());
        return kExitUsage;
    }
    const AnalysisResult result = analyze_stream(source);
    const std::string report = render_report(result, format);
    std::fwrite(report.data(), 1, report.size(), stdout);
    return result.has_recoverable_errors() ? kExitStreamErrors : kExitOk;
}

}  // namespace

int run(int argc, char** argv) {
    if (argc < 2) {
        print_usage(stderr);
        return kExitUsage;
    }
    const std::string command = argv[1];
    if (command == "--help" || command == "-h" || command == "help") {
        print_usage(stdout);
        return kExitOk;
    }
    if (command == "--generate-sample" || command == "--generate-interlaced-sample") {
        if (argc != 3) {
            std::fprintf(stderr, "error: %s requires exactly one output path\n",
                         command.c_str());
            return kExitUsage;
        }
        return run_generate(argv[2], command == "--generate-interlaced-sample");
    }
    if (command != "analyze") {
        std::fprintf(stderr, "error: unknown command '%s'\n", command.c_str());
        print_usage(stderr);
        return kExitUsage;
    }

    std::string path;
    ReportFormat format = ReportFormat::Text;
    for (int i = 2; i < argc; ++i) {
        const std::string arg = argv[i];
        if (arg == "--format") {
            if (i + 1 >= argc) {
                std::fprintf(stderr, "error: --format requires a value\n");
                return kExitUsage;
            }
            const int status = parse_format(argv[++i], format);
            if (status != kExitOk) return status;
        } else if (arg.rfind("--format=", 0) == 0) {
            const int status = parse_format(arg.substr(std::strlen("--format=")),
                                            format);
            if (status != kExitOk) return status;
        } else if (!arg.empty() && arg[0] == '-') {
            std::fprintf(stderr, "error: unknown option '%s'\n", arg.c_str());
            return kExitUsage;
        } else if (path.empty()) {
            path = arg;
        } else {
            std::fprintf(stderr, "error: unexpected argument '%s'\n", arg.c_str());
            return kExitUsage;
        }
    }
    if (path.empty()) {
        std::fprintf(stderr, "error: analyze requires an input file\n");
        return kExitUsage;
    }
    return run_analyze(path, format);
}

}  // namespace h264inspect

int main(int argc, char** argv) { return h264inspect::run(argc, argv); }
