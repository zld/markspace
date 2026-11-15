#include "fixture_writer.hpp"
#include "group_index.hpp"
#include "h264_parser.hpp"
#include "nal_scanner.hpp"

#include <cstdint>
#include <fstream>
#include <iostream>
#include <string>
#include <vector>

namespace {

constexpr int EXIT_OK = 0;
constexpr int EXIT_ARG = 1;
constexpr int EXIT_STRICT = 2;
constexpr int EXIT_RESOURCE = 3;

struct Options {
    std::string input;
    std::string format = "auto";
    bool json = false;
    bool strict = false;
    bool generateSample = false;
    bool help = false;
};

void printUsage(std::ostream& os) {
    os <<
      "h264_inspect - local H.264 bitstream checker\n"
      "Usage:\n"
      "  h264_inspect --input PATH [--format auto|annexb|avcc]\n"
      "               [--json] [--strict] [--generate-sample]\n"
      "  h264_inspect --help\n"
      "Exit codes: 0 ok, 1 argument error, 2 strict parse failure,\n"
      "            3 file/resource limit error\n";
}

bool parseArgs(int argc, char** argv, Options& o, std::string& err) {
    for (int i = 1; i < argc; ++i) {
        std::string a = argv[i];
        auto need = [&](std::string& dst) -> bool {
            if (i + 1 >= argc) { err = "missing value for " + a; return false; }
            dst = argv[++i];
            return true;
        };
        if (a == "--input") { if (!need(o.input)) return false; }
        else if (a == "--format") {
            if (!need(o.format)) return false;
            if (o.format != "auto" && o.format != "annexb" &&
                o.format != "avcc") {
                err = "invalid --format (want auto|annexb|avcc): " + o.format;
                return false;
            }
        }
        else if (a == "--json") o.json = true;
        else if (a == "--strict") o.strict = true;
        else if (a == "--generate-sample") o.generateSample = true;
        else if (a == "--help" || a == "-h") o.help = true;
        else { err = "unknown argument: " + a; return false; }
    }
    if (!o.help && !o.generateSample && o.input.empty()) {
        err = "--input is required (or use --generate-sample)";
        return false;
    }
    return true;
}

bool readFile(const std::string& path, std::vector<std::uint8_t>& buf,
              std::string& err) {
    std::ifstream f(path, std::ios::binary);
    if (!f) { err = "cannot open file: " + path; return false; }
    f.seekg(0, std::ios::end);
    std::streamoff len = f.tellg();
    if (len < 0) { err = "cannot determine file size: " + path; return false; }
    if (static_cast<std::uint64_t>(len) > h264::MAX_FILE_SIZE) {
        err = "file exceeds 512 MiB limit: " + path;
        return false;
    }
    f.seekg(0, std::ios::beg);
    buf.resize(static_cast<std::size_t>(len));
    if (len > 0 && !f.read(reinterpret_cast<char*>(buf.data()), len)) {
        err = "short read from: " + path;
        return false;
    }
    return true;
}

void printText(const h264::ScanResult& scan, const h264::H264Parser& parser,
               const h264::GroupIndex& idx, std::size_t inputBytes,
               int errorCount) {
    const h264::SpsData* sps = parser.activeSpsForOffset(inputBytes);
    std::cout << "h264_inspect report\n";
    std::cout << "format=" << h264::formatName(scan.format) << "\n";
    std::cout << "input_bytes=" << inputBytes << "\n";
    std::cout << "nals=" << scan.nals.size() << "\n";
    std::cout << "errors=" << errorCount << "\n";
    if (sps) {
        std::cout << "profile_idc=" << sps->profileIdc << "\n";
        std::cout << "level_idc=" << sps->levelIdc << "\n";
        std::cout << "width=" << sps->croppedWidth << "\n";
        std::cout << "height=" << sps->croppedHeight << "\n";
        std::cout << "width_uncropped=" << sps->frameWidth << "\n";
        std::cout << "height_uncropped=" << sps->frameHeight << "\n";
        std::cout << "interlaced="
                  << (!sps->frameMbsOnlyFlag ? "true" : "false") << "\n";
        std::cout << "frame_rate_fps=" << sps->frameRate << "\n";
    }
    std::cout << "frames={I:" << idx.totals.i << ",P:" << idx.totals.p
              << ",B:" << idx.totals.b << ",unknown:" << idx.totals.unknown
              << "}\n";
    std::cout << "access_units=" << idx.accessUnits.size() << "\n";
    std::cout << "groups=" << idx.groups.size() << "\n";
    for (const auto& g : idx.groups)
        std::cout << "group start_offset=" << g.startOffset
                  << " first_access_unit=" << g.firstAccessUnit
                  << " frame_count=" << g.frameCount << "\n";
}

} // namespace

int main(int argc, char** argv) {
    Options opts;
    std::string argErr;
    if (!parseArgs(argc, argv, opts, argErr)) {
        std::cerr << "argument error: " << argErr << "\n";
        printUsage(std::cerr);
        return EXIT_ARG;
    }
    if (opts.help) { printUsage(std::cout); return EXIT_OK; }

    std::vector<std::uint8_t> data;
    if (opts.generateSample) {
        h264::FixtureWriter fw;
        data = fw.buildSample();
        if (!opts.input.empty()) {
            std::ofstream out(opts.input, std::ios::binary);
            if (!out) {
                std::cerr << "resource error: cannot write " << opts.input
                          << "\n";
                return EXIT_RESOURCE;
            }
            out.write(reinterpret_cast<const char*>(data.data()),
                      static_cast<std::streamsize>(data.size()));
        }
    } else {
        std::string err;
        if (!readFile(opts.input, data, err)) {
            std::cerr << "resource error: " << err << "\n";
            return EXIT_RESOURCE;
        }
    }

    h264::ScanResult scan =
        h264::scanNals(data.data(), data.size(), opts.format, !opts.strict);
    h264::H264Parser parser;
    for (const auto& nal : scan.nals)
        parser.feed(nal, data.data(), !opts.strict);
    h264::GroupIndex idx = h264::buildGroupIndex(scan, parser);
    int errorCount =
        static_cast<int>(scan.errors.size() + parser.errors().size());

    // JSON output and fatal-format classification arrive in the next commit.
    printText(scan, parser, idx, data.size(), errorCount);
    if (opts.strict && errorCount > 0) return EXIT_STRICT;
    return EXIT_OK;
}
