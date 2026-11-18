// Annex B start-code scanning and NAL unit boundaries.
#pragma once

#include <cstdint>
#include <vector>

#include "byte_source.hpp"

namespace h264inspect {

// Limitation for a single NAL payload; larger units are reported and skipped.
constexpr std::uint32_t kMaxNalSize = 16u * 1024u * 1024u;

enum class NalUnitType : std::uint8_t {
    Unspecified = 0,
    NonIdrSlice = 1,
    SliceDataPartitionA = 2,
    SliceDataPartitionB = 3,
    SliceDataPartitionC = 4,
    IdrSlice = 5,
    Sei = 6,
    Sps = 7,
    Pps = 8,
    AccessUnitDelimiter = 9,
    EndOfSequence = 10,
    EndOfStream = 11,
    FillerData = 12,
    SpsExtension = 13,
    PrefixNalUnit = 14,
    SubsetSps = 15,
    AuxiliarySlice = 19,
    SliceExtension = 20,
    SliceExtensionDepth = 21,
};

const char* nal_unit_type_name(int type);

struct NalUnit {
    std::uint64_t start_code_offset = 0;  // offset of the first start-code byte
    std::uint32_t start_code_length = 0;  // 3 or 4
    std::uint64_t payload_offset = 0;     // offset of nal_header byte
    std::uint64_t payload_length = 0;     // bytes after nal_header
    std::uint8_t header = 0;

    bool forbidden_zero_bit() const { return (header & 0x80u) != 0; }
    int nal_ref_idc() const { return (header >> 5) & 0x03u; }
    int unit_type() const { return header & 0x1Fu; }
    bool is_slice() const {
        const int t = unit_type();
        return t == 1 || t == 2 || t == 5;
    }
    // VCL types whose header layout differs from the base slice header this
    // analyzer understands (partitions B/C, auxiliary, SVC/MVC extensions).
    bool is_unsupported_vcl() const {
        const int t = unit_type();
        return t == 3 || t == 4 || t == 19 || t == 20 || t == 21;
    }
    bool is_vcl() const { return is_slice() || is_unsupported_vcl(); }
};

struct NalScanSummary {
    std::uint64_t leading_garbage_bytes = 0;  // non-zero bytes before the first code
    std::uint64_t leading_zero_bytes = 0;     // zero padding before the first code
    std::uint32_t consecutive_start_codes = 0;  // back-to-back codes forming no NAL
    std::uint32_t empty_nal_units = 0;          // header-only (zero payload) NALs
    std::uint32_t inter_nal_zero_bytes = 0;     // zero padding between NAL units
    std::uint32_t trailing_zero_bytes = 0;      // zero padding after the last NAL
    std::uint64_t total_bytes = 0;
};

struct NalScanResult {
    std::vector<NalUnit> units;
    std::vector<Diagnostic> diagnostics;  // non-fatal structural findings
    NalScanSummary summary;
};

// Scans the whole source for Annex B start codes. Never throws on malformed
// content: structural oddities are recorded in diagnostics/summary instead.
NalScanResult scan_nal_units(const ByteSource& source);

}  // namespace h264inspect
