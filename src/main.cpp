#include "access_unit.hpp"
#include "report.hpp"

#include <cstdint>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <string>
#include <vector>

// Fixture generator lives in the tests tree; reuse it for make-fixture.
#include "bit_writer.hpp"

namespace {

constexpr std::uint64_t kMaxInputBytes = 512ull * 1024 * 1024;
constexpr int kExitOk = 0;
constexpr int kExitStrict = 2;
constexpr int kExitUsage = 64;
constexpr int kExitIo = 66;

struct Options {
    std::string command;
    std::string input;
    std::string output;
    bool json = false;
    bool strict = false;
};

void print_usage() {
    std::fprintf(stderr,
                 "usage:\n"
                 "  h264scan analyze <file.264> [--json] [--strict]\n"
                 "  h264scan make-fixture --output <file.264>\n");
}

// Returns an empty vector with `error` set on any failure; the 512 MiB cap
// keeps a malformed size claim from forcing an uncontrolled allocation.
std::vector<std::uint8_t> read_file(const std::string& path,
                                    std::string& error) {
    std::ifstream file(path, std::ios::binary | std::ios::ate);
    if (!file) {
        error = "cannot open input file: " + path;
        return {};
    }
    const std::streamoff length = file.tellg();
    if (length < 0) {
        error = "cannot determine size of input file: " + path;
        return {};
    }
    if (static_cast<std::uint64_t>(length) > kMaxInputBytes) {
        error = "input exceeds 512 MiB analysis limit: " + path;
        return {};
    }
    file.seekg(0, std::ios::beg);
    std::vector<std::uint8_t> buffer(static_cast<std::size_t>(length));
    if (length > 0 && !file.read(reinterpret_cast<char*>(buffer.data()),
                                 length)) {
        error = "failed reading input file: " + path;
        return {};
    }
    return buffer;
}

int run_analyze(const Options& opt) {
    std::string error;
    std::vector<std::uint8_t> buffer = read_file(opt.input, error);
    if (!error.empty()) {
        std::fprintf(stderr, "h264scan: %s\n", error.c_str());
        return kExitIo;
    }
    if (buffer.empty()) {
        std::fprintf(stderr,
                     "h264scan: input file is empty (offset 0): %s\n",
                     opt.input.c_str());
        return kExitIo;
    }

    h264::StreamSummary summary =
        h264::analyze_stream(buffer.data(), buffer.size());

    bool no_nal = false;
    if (summary.images.empty() && summary.groups.empty() &&
        !summary.sps.present && summary.unsupported_nal_count == 0 &&
        !summary.diagnostics.has_error()) {
        no_nal = true;
    }
    if (no_nal) {
        std::fprintf(stderr,
                     "h264scan: no NAL unit found in input (offset 0): %s\n",
                     opt.input.c_str());
        return kExitIo;
    }

    const std::string rendered =
        opt.json ? h264::render_json(summary) : h264::render_text(summary);
    std::fputs(rendered.c_str(), stdout);

    if (opt.strict && (summary.diagnostics.has_error() ||
                       summary.diagnostics.has_warning())) {
        return kExitStrict;
    }
    return kExitOk;
}

int run_make_fixture(const Options& opt) {
    if (opt.output.empty()) {
        print_usage();
        return kExitUsage;
    }
    const auto stream = h264::gen::build_demo_stream();
    std::ofstream file(opt.output, std::ios::binary | std::ios::trunc);
    if (!file) {
        std::fprintf(stderr, "h264scan: cannot open output file: %s\n",
                     opt.output.c_str());
        return kExitIo;
    }
    file.write(reinterpret_cast<const char*>(stream.data()),
               static_cast<std::streamsize>(stream.size()));
    if (!file) {
        std::fprintf(stderr, "h264scan: write failed: %s\n",
                     opt.output.c_str());
        return kExitIo;
    }
    std::printf("wrote %zu fixture bytes to %s\n", stream.size(),
                opt.output.c_str());
    return kExitOk;
}

}  // namespace

int main(int argc, char** argv) {
    if (argc < 2) {
        print_usage();
        return kExitUsage;
    }
    Options opt;
    opt.command = argv[1];

    if (opt.command == "make-fixture") {
        for (int i = 2; i < argc; ++i) {
            if (std::strcmp(argv[i], "--output") == 0 && i + 1 < argc) {
                opt.output = argv[++i];
            } else {
                print_usage();
                return kExitUsage;
            }
        }
        return run_make_fixture(opt);
    }

    if (opt.command != "analyze") {
        print_usage();
        return kExitUsage;
    }
    for (int i = 2; i < argc; ++i) {
        if (std::strcmp(argv[i], "--json") == 0) {
            opt.json = true;
        } else if (std::strcmp(argv[i], "--strict") == 0) {
            opt.strict = true;
        } else if (argv[i][0] != '-' && opt.input.empty()) {
            opt.input = argv[i];
        } else {
            print_usage();
            return kExitUsage;
        }
    }
    if (opt.input.empty()) {
        print_usage();
        return kExitUsage;
    }
    return run_analyze(opt);
}
