#pragma once

#include "diagnostic.hpp"

#include <cstddef>
#include <cstdint>
#include <vector>

namespace h264 {

enum NalUnitType : std::uint32_t {
    kNalNonIdrSlice = 1,
    kNalPartitionA = 2,
    kNalPartitionB = 3,
    kNalPartitionC = 4,
    kNalIdrSlice = 5,
    kNalSei = 6,
    kNalSps = 7,
    kNalPps = 8,
    kNalAccessUnitDelimiter = 9,
    kNalEndOfSequence = 10,
    kNalEndOfStream = 11,
    kNalFiller = 12,
    kNalSpsExtension = 13,
};

struct NalUnit {
    // Byte offset of the first byte of the start code prefix.  For a
    // four-byte prefix this is the leading zero_byte.
    std::uint64_t start_offset = 0;
    // 3 for 00 00 01, 4 for 00 00 00 01.
    unsigned prefix_length = 0;
    // Offset of the NAL header byte and of the first payload byte.
    std::uint64_t header_offset = 0;
    std::uint64_t payload_offset = 0;
    // Payload size in bytes after the header, excluding trailing zero
    // padding; content is still NAL-escaped (use rbsp_unescape).
    std::size_t payload_size = 0;

    bool empty = false;  // no header byte was present between start codes
    bool forbidden_zero_bit = false;
    unsigned nal_ref_idc = 0;
    std::uint32_t unit_type = 0;

    bool is_slice() const {
        return unit_type == kNalNonIdrSlice || unit_type == kNalIdrSlice;
    }
};

struct ScanResult {
    std::vector<NalUnit> nal_units;
    DiagnosticSink diagnostics;
    // Number of bytes before the first start code.
    std::uint64_t leading_bytes = 0;
    bool found_start_code = false;
    std::size_t stream_bytes = 0;
};

// Scans an Annex B byte stream.
//
// Handles 3-byte and 4-byte start codes, arbitrary runs of leading zero
// bytes, consecutive start codes (which yield an empty NAL), a final NAL
// running to EOF without trailing zeros, and a truncated start code at EOF.
// Input is consumed monotonically and the number of emitted NAL units is
// capped, so malformed data cannot loop or trigger unbounded allocation.
// All offsets are byte offsets into the caller's buffer.
ScanResult scan_annex_b(const std::uint8_t* data, std::size_t size);

// Human-readable name for a NAL unit type.
const char* nal_unit_type_name(std::uint32_t unit_type);

}  // namespace h264
