#include "nal_scanner.hpp"

namespace h264 {
namespace {

constexpr std::size_t kMaxNalUnits = 4'000'000;

bool is_three_byte_code(const std::uint8_t* p) {
    return p[0] == 0x00 && p[1] == 0x00 && p[2] == 0x01;
}

}  // namespace

ScanResult scan_annex_b(const std::uint8_t* data, std::size_t size) {
    ScanResult result;
    result.stream_bytes = size;
    if (size == 0) {
        return result;
    }

    // Locate every 00 00 01 three-byte start code.  The zero_byte of a
    // four-byte form and any longer zero run are folded into the prefix by
    // walking backwards from each match.  The walk is bounded per match, so
    // total work stays linear in the number of input bytes.
    struct Start {
        std::size_t prefix_begin;
        unsigned prefix_length;
        std::size_t nal_begin;  // first byte after the prefix (NAL header)
    };
    std::vector<Start> starts;
    for (std::size_t i = 0; i + 2 < size; ++i) {
        if (!is_three_byte_code(data + i)) {
            continue;
        }
        std::size_t begin = i;
        while (begin > 0 && data[begin - 1] == 0x00) {
            --begin;
        }
        const std::size_t run = i + 3 - begin;  // zero run incl. the 01
        starts.push_back(
            {begin, run >= 4 ? 4u : 3u, i + 3});
        i += 2;  // next possible code starts after this 00 00 01
    }

    if (starts.empty()) {
        // A dangling run of two or more zeros looks like a cut start code.
        std::size_t trail = size;
        while (trail > 0 && data[trail - 1] == 0x00) {
            --trail;
        }
        if (size - trail >= 2) {
            result.diagnostics.error(
                trail, "truncated_start_code",
                "stream ends with zero run but no 00 00 01 start code");
        }
        return result;
    }

    result.found_start_code = true;
    result.leading_bytes = starts.front().prefix_begin;
    if (result.leading_bytes > 0) {
        bool only_zeros = true;
        for (std::uint64_t i = 0; i < result.leading_bytes; ++i) {
            if (data[i] != 0x00) {
                only_zeros = false;
                break;
            }
        }
        if (!only_zeros) {
            result.diagnostics.warning(
                0, "leading_garbage",
                "non-zero bytes precede the first start code and are ignored");
        }
    }

    for (std::size_t k = 0; k < starts.size(); ++k) {
        const std::size_t nal_begin = starts[k].nal_begin;
        const std::size_t next_prefix =
            (k + 1 < starts.size()) ? starts[k + 1].prefix_begin : size;

        // Strip trailing zero padding: zeros directly before the next start
        // code (or EOF) belong to the prefix/padding, not this NAL.
        std::size_t nal_end = next_prefix;
        while (nal_end > nal_begin && data[nal_end - 1] == 0x00) {
            --nal_end;
        }

        NalUnit unit;
        unit.start_offset = starts[k].prefix_begin;
        unit.prefix_length = starts[k].prefix_length;
        unit.header_offset = nal_begin;
        unit.payload_offset = nal_begin + 1;

        if (nal_end <= nal_begin) {
            // Consecutive start codes (or a start code immediately followed
            // by another prefix): the NAL has no header byte.
            unit.empty = true;
            unit.payload_size = 0;
            result.diagnostics.error(
                unit.start_offset, "empty_nal",
                "NAL unit contains no header byte");
        } else {
            const std::uint8_t header = data[nal_begin];
            unit.forbidden_zero_bit = (header & 0x80) != 0;
            unit.nal_ref_idc = (header >> 5) & 0x03;
            unit.unit_type = header & 0x1f;
            unit.payload_size = nal_end - nal_begin - 1;
        }
        result.nal_units.push_back(std::move(unit));

        if (result.nal_units.size() >= kMaxNalUnits) {
            result.diagnostics.error(
                next_prefix, "nal_limit",
                "NAL unit count limit reached; remaining input not scanned");
            return result;
        }
    }

    // Truncated start code after the last complete NAL: a dangling zero run
    // of at least two bytes with no terminating 01 byte.
    std::size_t trail = size;
    while (trail > 0 && data[trail - 1] == 0x00) {
        --trail;
    }
    if (trail > starts.back().nal_begin && size - trail >= 2) {
        result.diagnostics.error(
            trail, "truncated_start_code",
            "trailing zero run at end of file looks like a cut start code");
    }

    return result;
}

const char* nal_unit_type_name(std::uint32_t unit_type) {
    switch (unit_type) {
        case 1: return "non-IDR slice";
        case 2: return "slice data partition A";
        case 3: return "slice data partition B";
        case 4: return "slice data partition C";
        case 5: return "IDR slice";
        case 6: return "SEI";
        case 7: return "SPS";
        case 8: return "PPS";
        case 9: return "access unit delimiter";
        case 10: return "end of sequence";
        case 11: return "end of stream";
        case 12: return "filler";
        case 13: return "SPS extension";
        default: return "unspecified";
    }
}

}  // namespace h264
