#ifndef H264_SUMMARY_WRITER_HPP
#define H264_SUMMARY_WRITER_HPP

#include <cstdint>
#include <iosfwd>
#include <string>
#include <vector>

#include "access_unit.hpp"
#include "diagnostics.hpp"
#include "nal_parser.hpp"
#include "parameter_sets.hpp"

namespace h264 {

// Everything the report needs, assembled by the driver while it streams the
// file. Keeping it a plain value object lets text and JSON renderers share
// exactly one source of truth.
struct ScanSummary {
    std::string input_path;
    std::uint64_t file_size = 0;
    std::uint64_t max_nal_bytes = 0;
    std::size_t nal_count = 0;
    std::vector<NalMeta> nals;          // metadata only; RBSP payloads dropped
    ParameterSetStore::SpsMap sps_map;
    ParameterSetStore::PpsMap pps_map;
    FrameStats frames;
    std::vector<PictureInfo> pictures;
    std::vector<PictureInfo> leading;
    std::vector<GopInfo> groups;
    const Diagnostics* diagnostics = nullptr;
};

// Human-readable report for terminal review.
void write_text_summary(const ScanSummary& summary, std::ostream& out);

// Machine-readable report for regression scripts. All strings are JSON
// escaped; byte offsets are emitted as unsigned integers.
void write_json_summary(const ScanSummary& summary, std::ostream& out);

}  // namespace h264

#endif
