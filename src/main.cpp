#include <algorithm>
#include <cstdint>
#include <cstdlib>
#include <fstream>
#include <iostream>
#include <string>
#include <vector>

#include "access_unit.hpp"
#include "diagnostics.hpp"
#include "nal_parser.hpp"
#include "parameter_sets.hpp"
#include "summary_writer.hpp"

namespace {

// Exit codes per the tool contract.
constexpr int kOk = 0;
constexpr int kStrictErrors = 2;
constexpr int kUsageError = 64;
constexpr int kIoError = 70;

struct Options {
    std::string input;
    bool json = false;
    bool strict = false;
    std::uint64_t max_nal_bytes = h264::NalScanner::kDefaultMaxNalBytes;
};

void print_usage(std::ostream& out) {
    out << "usage: h264scan --input FILE [--json] [--strict] "
           "[--max-nal-bytes N]\n";
}

// Parses a size argument requiring a positive integer within 64-bit range.
bool parse_size(const std::string& text, std::uint64_t& value) {
    if (text.empty()) return false;
    std::uint64_t v = 0;
    for (char c : text) {
        if (c < '0' || c > '9') return false;
        const unsigned digit = static_cast<unsigned>(c - '0');
        if (v > (UINT64_MAX - digit) / 10ull) return false;  // overflow
        v = v * 10ull + digit;
    }
    if (v == 0) return false;
    value = v;
    return true;
}

int parse_args(int argc, char** argv, Options& opt) {
    for (int i = 1; i < argc; ++i) {
        const std::string arg = argv[i];
        auto need_value = [&](std::string& target) -> bool {
            if (i + 1 >= argc) {
                std::cerr << "error: " << arg << " requires a value\n";
                return false;
            }
            target = argv[++i];
            return true;
        };

        if (arg == "--json") {
            opt.json = true;
        } else if (arg == "--strict") {
            opt.strict = true;
        } else if (arg == "--input") {
            std::string v;
            if (!need_value(v)) return kUsageError;
            opt.input = v;
        } else if (arg.rfind("--input=", 0) == 0) {
            opt.input = arg.substr(8);
        } else if (arg == "--max-nal-bytes") {
            std::string v;
            if (!need_value(v)) return kUsageError;
            if (!parse_size(v, opt.max_nal_bytes)) {
                std::cerr << "error: --max-nal-bytes must be a positive "
                             "integer: " << v << "\n";
                return kUsageError;
            }
        } else if (arg.rfind("--max-nal-bytes=", 0) == 0) {
            if (!parse_size(arg.substr(16), opt.max_nal_bytes)) {
                std::cerr << "error: --max-nal-bytes must be a positive "
                             "integer\n";
                return kUsageError;
            }
        } else if (arg == "--help" || arg == "-h") {
            print_usage(std::cout);
            std::exit(kOk);
        } else {
            std::cerr << "error: unknown argument: " << arg << "\n";
            print_usage(std::cerr);
            return kUsageError;
        }
    }

    if (opt.input.empty()) {
        std::cerr << "error: --input is required\n";
        print_usage(std::cerr);
        return kUsageError;
    }
    constexpr std::uint64_t kMinNalCap = 64ull;
    constexpr std::uint64_t kMaxNalCap = 1ull << 30;  // 1 GiB sanity ceiling
    if (opt.max_nal_bytes < kMinNalCap || opt.max_nal_bytes > kMaxNalCap) {
        std::cerr << "error: --max-nal-bytes must be between 64 and "
                  << kMaxNalCap << "\n";
        return kUsageError;
    }
    return kOk;
}

}  // namespace

int main(int argc, char** argv) {
    Options opt;
    const int arg_rc = parse_args(argc, argv, opt);
    if (arg_rc != kOk) return arg_rc;

    std::ifstream file(opt.input, std::ios::binary);
    if (!file) {
        std::cerr << "error: cannot open input file: " << opt.input << "\n";
        return kIoError;
    }

    h264::Diagnostics diag;
    h264::NalScanner scanner(diag, opt.max_nal_bytes);
    h264::ParameterSetStore store;
    h264::AccessUnitAssembler assembler(store);
    h264::ScanSummary summary;
    summary.input_path = opt.input;
    summary.max_nal_bytes = opt.max_nal_bytes;
    summary.diagnostics = &diag;

    constexpr std::size_t kChunk = 64u * 1024u;
    std::vector<std::uint8_t> chunk(kChunk);
    std::vector<h264::NalUnit> ready;

    auto dispatch = [&](h264::NalUnit& nal) {
        summary.nal_count++;
        summary.nals.push_back(h264::nal_meta(nal));
        if (nal.nal_type == h264::NAL_SPS && nal.valid) {
            h264::SpsInfo sps;
            if (h264::parse_sps(nal, sps, diag)) store.add_sps(sps);
        } else if (nal.nal_type == h264::NAL_PPS && nal.valid) {
            h264::PpsInfo pps;
            if (h264::parse_pps(nal, pps, diag)) store.add_pps(pps);
        } else {
            assembler.handle_nal(nal, diag);
        }
    };

    // Fixed-size read buffer plus single buffered NAL: memory use is
    // independent of total file length.
    while (file) {
        file.read(reinterpret_cast<char*>(chunk.data()),
                  static_cast<std::streamsize>(chunk.size()));
        const std::streamsize got = file.gcount();
        if (got > 0) {
            scanner.feed(chunk.data(), static_cast<std::size_t>(got), ready);
        }
        for (h264::NalUnit& nal : ready) dispatch(nal);
        ready.clear();
        if (file.bad()) {
            std::cerr << "error: read failure on " << opt.input << "\n";
            return kIoError;
        }
    }
    scanner.finish(ready);
    for (h264::NalUnit& nal : ready) dispatch(nal);
    assembler.finish(diag);
    summary.file_size = scanner.total_bytes();
    summary.sps_map = store.all_sps();
    summary.pps_map = store.all_pps();
    summary.frames = assembler.stats();
    summary.pictures = assembler.pictures();
    summary.leading = assembler.leading_pictures();
    summary.groups = assembler.groups();

    for (const h264::Diag& d : diag) {
        std::cerr << "h264scan: @" << d.offset << ": " << d.message << "\n";
    }

    if (opt.json) {
        h264::write_json_summary(summary, std::cout);
    } else {
        h264::write_text_summary(summary, std::cout);
    }
    if (!std::cout.good()) {
        std::cerr << "error: failed writing summary\n";
        return kIoError;
    }

    if (opt.strict && diag.has_errors()) return kStrictErrors;
    return kOk;
}
