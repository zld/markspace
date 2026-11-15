#ifndef H264_GROUP_INDEX_HPP
#define H264_GROUP_INDEX_HPP

#include "h264_parser.hpp"
#include "nal_scanner.hpp"

#include <cstddef>
#include <vector>

namespace h264 {

struct FrameCount {
    int i = 0, p = 0, b = 0, unknown = 0;
    int total() const { return i + p + b + unknown; }
};

// One decoded access unit (frame; in interlaced streams two complementary
// fields may share an AU — kept as a single entry).
struct AccessUnit {
    int index = 0;                  // 0-based AU number in stream order
    std::size_t firstNalOffset = 0; // offset of first NAL of the AU
    FrameType type = FT_UNKNOWN;
    int frameNum = -1;
    bool idr = false;
    bool interlaced = false;        // at least one slice carries field_pic_flag
    bool unknownType = false;       // no parameter set / unparsable header
    int sliceCount = 0;
    bool incomplete = false;        // trailing unit without a full slice header
};

struct Group {
    std::size_t startOffset = 0;    // byte offset of first NAL in the group
    int firstAccessUnit = 0;        // AU index that opens the group
    int frameCount = 0;
    FrameCount types;
    bool hasLeadingNonVcl = false;  // pre-VCL parameter/SEI/AUD NALs
    bool openedByIdr = false;
    bool hasParamSetUpdate = false;
    bool incomplete = false;        // unterminated group at end of stream
};

struct GroupIndex {
    std::vector<AccessUnit> accessUnits;
    std::vector<Group> groups;
    FrameCount totals;
    int skippedNals = 0;
    std::size_t firstErrorOffset = static_cast<std::size_t>(-1);
};

// Aggregate scanned NALs + parsed slice headers into access units and groups.
GroupIndex buildGroupIndex(const ScanResult& scan,
                           const H264Parser& parser);

} // namespace h264

#endif
