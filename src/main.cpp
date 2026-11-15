#include "fixture_writer.hpp"
#include "group_index.hpp"
#include "h264_parser.hpp"
#include "nal_scanner.hpp"

#include <cstdint>
#include <cstdio>
#include <fstream>
#include <iostream>
#include <sstream>
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
            if (i + 1 >= argc) {
                err = "missing value for " + a;
                return false;
            }
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

std::string jsonEscape(const std::string& s) {
    std::string o;
    o.reserve(s.size());
    for (char c : s) {
        switch (c) {
            case '"': o += "\\\""; break;
            case '\\': o += "\\\\"; break;
            case '\n': o += "\\n"; break;
            case '\r': o += "\\r"; break;
            case '\t': o += "\\t"; break;
            default: o += c;
        }
    }
    return o;
}

std::string readFileOrError(const std::string& path,
                            std::vector<std::uint8_t>& buf,
                            std::string& err) {
    std::ifstream f(path, std::ios::binary);
    if (!f) { err = "cannot open file: " + path; return "file"; }
    f.seekg(0, std::ios::end);
    std::streamoff len = f.tellg();
    if (len < 0) { err = "cannot determine file size: " + path; return "file"; }
    if (static_cast<std::uint64_t>(len) > h264::MAX_FILE_SIZE) {
        err = "file exceeds 512 MiB limit: " + path;
        return "resource";
    }
    f.seekg(0, std::ios::beg);
    buf.resize(static_cast<std::size_t>(len));
    if (len > 0 && !f.read(reinterpret_cast<char*>(buf.data()), len)) {
        err = "short read from: " + path;
        return "resource";
    }
    return "";
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
        std::string kind = readFileOrError(opts.input, data, err);
        if (!kind.empty()) {
            std::cerr << kind << " error: " << err << "\n";
            return EXIT_RESOURCE;
        }
    }

    h264::ScanResult scan = h264::scanNals(
        data.data(), data.size(), opts.format, !opts.strict);

    // Format-level failures (empty input, undetectable framing, conflicting
    // hint, zero usable units) are fatal even in tolerant mode: there is no
    // stream structure to recover. The report is still emitted below.
    bool fatalFormatError =
        scan.format == h264::StreamFormat::Unknown || scan.nals.empty();

    h264::H264Parser parser;
    for (const auto& nal : scan.nals)
        parser.feed(nal, data.data(), !opts.strict);

    h264::GroupIndex idx = h264::buildGroupIndex(scan, parser);
    int errorCount = static_cast<int>(scan.errors.size() +
                                      parser.errors().size());
    if (idx.totals.total() !=
        static_cast<int>(idx.accessUnits.size())) {
        // Internal invariant: frame counts must cover every access unit.
        std::cerr << "invariant error: frame counts (" << idx.totals.total()
                  << ") differ from access units ("
                  << idx.accessUnits.size() << ")\n";
        return EXIT_STRICT;
    }

    const h264::SpsData* sps = parser.activeSpsForOffset(data.size());

    if (opts.json) {
        std::ostringstream j;
        j << "{\n";
        j << "  \"format\": \"" << h264::formatName(scan.format) << "\",\n";
        j << "  \"input_bytes\": " << data.size() << ",\n";
        j << "  \"nals\": " << scan.nals.size() << ",\n";
        j << "  \"errors\": " << errorCount << ",\n";
        j << "  \"strict\": " << (opts.strict ? "true" : "false") << ",\n";
        j << "  \"sps\": {\n";
        if (sps) {
            j << "    \"sps_id\": " << sps->spsId << ",\n";
            j << "    \"profile_idc\": " << sps->profileIdc << ",\n";
            j << "    \"level_idc\": " << sps->levelIdc << ",\n";
            j << "    \"constraint_flags\": " << sps->constraintFlags
              << ",\n";
            j << "    \"width_mb_units\": " << sps->widthMbUnits << ",\n";
            j << "    \"height_map_units\": " << sps->heightMapUnits
              << ",\n";
            j << "    \"width\": " << sps->croppedWidth << ",\n";
            j << "    \"height\": " << sps->croppedHeight << ",\n";
            j << "    \"frame_width_uncropped\": " << sps->frameWidth
              << ",\n";
            j << "    \"frame_height_uncropped\": " << sps->frameHeight
              << ",\n";
            j << "    \"crop_left\": " << sps->cropLeft
              << ", \"crop_right\": " << sps->cropRight
              << ", \"crop_top\": " << sps->cropTop
              << ", \"crop_bottom\": " << sps->cropBottom << ",\n";
            j << "    \"frame_mbs_only_flag\": "
              << (sps->frameMbsOnlyFlag ? "true" : "false") << ",\n";
            j << "    \"interlaced\": "
              << (!sps->frameMbsOnlyFlag ? "true" : "false") << ",\n";
            j << "    \"log2_max_frame_num\": " << sps->log2MaxFrameNum
              << ",\n";
            j << "    \"pic_order_cnt_type\": " << sps->picOrderCntType
              << ",\n";
            j << "    \"max_num_ref_frames\": " << sps->maxNumRefFrames
              << ",\n";
            j << "    \"timing_info_present\": "
              << (sps->timingInfoPresent ? "true" : "false") << ",\n";
            j << "    \"fixed_frame_rate\": "
              << (sps->fixedFrameRateFlag ? "true" : "false") << ",\n";
            j << "    \"num_units_in_tick\": " << sps->numUnitsInTick
              << ",\n";
            j << "    \"time_scale\": " << sps->timeScale << ",\n";
            j << "    \"frame_rate_fps\": " << sps->frameRate << "\n";
        }
        j << "  },\n";
        j << "  \"frames\": {\"I\":" << idx.totals.i
          << ", \"P\":" << idx.totals.p
          << ", \"B\":" << idx.totals.b
          << ", \"unknown\":" << idx.totals.unknown << "},\n";
        j << "  \"access_units\": " << idx.accessUnits.size() << ",\n";
        j << "  \"groups\": " << idx.groups.size() << ",\n";
        j << "  \"group_details\": [\n";
        for (std::size_t g = 0; g < idx.groups.size(); ++g) {
            const auto& gr = idx.groups[g];
            j << "    {\"start_offset\": " << gr.startOffset
              << ", \"first_access_unit\": " << gr.firstAccessUnit
              << ", \"frame_count\": " << gr.frameCount
              << ", \"frames\": {\"I\":" << gr.types.i
              << ", \"P\":" << gr.types.p
              << ", \"B\":" << gr.types.b
              << ", \"unknown\":" << gr.types.unknown << "}"
              << ", \"leading_non_vcl\": "
              << (gr.hasLeadingNonVcl ? "true" : "false")
              << ", \"opened_by_idr\": "
              << (gr.openedByIdr ? "true" : "false")
              << ", \"param_set_update\": "
              << (gr.hasParamSetUpdate ? "true" : "false")
              << ", \"incomplete\": "
              << (gr.incomplete ? "true" : "false") << "}"
              << (g + 1 < idx.groups.size() ? "," : "") << "\n";
        }
        j << "  ],\n";
        j << "  \"skipped_nals\": " << idx.skippedNals << ",\n";
        j << "  \"first_error_offset\": "
          << (idx.firstErrorOffset == static_cast<std::size_t>(-1)
                  ? -1 : static_cast<long long>(idx.firstErrorOffset))
          << ",\n";
        j << "  \"error_details\": [";
        int emitted = 0;
        for (const auto& e : scan.errors) {
            j << (emitted++ ? ", " : "") << "{\"stage\":\"scan\", \"offset\":"
              << e.offset << ", \"reason\":\"" << jsonEscape(e.reason)
              << "\"}";
        }
        for (const auto& e : parser.errors()) {
            j << (emitted++ ? ", " : "") << "{\"stage\":\""
              << jsonEscape(e.stage) << "\", \"offset\":" << e.offset
              << ", \"reason\":\"" << jsonEscape(e.reason) << "\"}";
        }
        j << "],\n";
        j << "  \"param_set_events\": [";
        for (std::size_t k = 0; k < parser.events().size(); ++k) {
            const auto& ev = parser.events()[k];
            const char* kind = "other";
            switch (ev.kind) {
                case h264::EventKind::SpsNew: kind = "sps_new"; break;
                case h264::EventKind::SpsDuplicate: kind = "sps_duplicate"; break;
                case h264::EventKind::SpsUpdate: kind = "sps_update"; break;
                case h264::EventKind::PpsNew: kind = "pps_new"; break;
                case h264::EventKind::PpsDuplicate: kind = "pps_duplicate"; break;
                case h264::EventKind::PpsUpdate: kind = "pps_update"; break;
                case h264::EventKind::PpsMissingSps: kind = "pps_missing_sps"; break;
            }
            j << (k ? ", " : "") << "{\"kind\":\"" << kind
              << "\", \"id\":" << ev.id << ", \"offset\":" << ev.offset
              << ", \"detail\":\"" << jsonEscape(ev.detail) << "\"}";
        }
        j << "]\n";
        j << "}\n";
        std::cout << j.str();
    } else {
        std::cout << "h264_inspect report\n";
        std::cout << "format=" << h264::formatName(scan.format) << "\n";
        std::cout << "input_bytes=" << data.size() << "\n";
        std::cout << "nals=" << scan.nals.size() << "\n";
        std::cout << "errors=" << errorCount << "\n";
        if (sps) {
            std::cout << "profile_idc=" << sps->profileIdc << "\n";
            std::cout << "level_idc=" << sps->levelIdc << "\n";
            std::cout << "width=" << sps->croppedWidth << "\n";
            std::cout << "height=" << sps->croppedHeight << "\n";
            std::cout << "width_uncropped=" << sps->frameWidth << "\n";
            std::cout << "height_uncropped=" << sps->frameHeight << "\n";
            std::cout << "width_mb_units=" << sps->widthMbUnits << "\n";
            std::cout << "height_map_units=" << sps->heightMapUnits << "\n";
            std::cout << "interlaced="
                      << (!sps->frameMbsOnlyFlag ? "true" : "false") << "\n";
            std::cout << "frame_rate_fps=" << sps->frameRate << "\n";
        }
        std::cout << "frames={I:" << idx.totals.i
                  << ",P:" << idx.totals.p
                  << ",B:" << idx.totals.b
                  << ",unknown:" << idx.totals.unknown << "}\n";
        std::cout << "access_units=" << idx.accessUnits.size() << "\n";
        std::cout << "groups=" << idx.groups.size() << "\n";
        for (const auto& g : idx.groups) {
            std::cout << "group start_offset=" << g.startOffset
                      << " first_access_unit=" << g.firstAccessUnit
                      << " frame_count=" << g.frameCount
                      << " I=" << g.types.i << " P=" << g.types.p
                      << " B=" << g.types.b
                      << " unknown=" << g.types.unknown
                      << (g.openedByIdr ? " idr" : "")
                      << (g.incomplete ? " incomplete" : "") << "\n";
        }
        for (const auto& e : scan.errors)
            std::cout << "error stage=scan offset=" << e.offset
                      << " reason=" << e.reason << "\n";
        for (const auto& e : parser.errors())
            std::cout << "error stage=" << e.stage << " offset=" << e.offset
                      << " reason=" << e.reason << "\n";
        for (const auto& ev : parser.events())
            std::cout << "event offset=" << ev.offset << " " << ev.detail
                      << "\n";
    }

    if (opts.strict && errorCount > 0) return EXIT_STRICT;
    if (fatalFormatError) return EXIT_STRICT;
    return EXIT_OK;
}
