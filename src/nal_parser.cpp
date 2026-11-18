#include "nal_parser.hpp"

#include <cstddef>

namespace h264inspect {

namespace {

// Returns the start-code length at `pos` (3 or 4) or 0 when absent.
// Four-byte codes are preferred so the reported offset is the first byte of
// the leading zero, matching how encoders align IDR access units.
std::uint32_t start_code_length_at(const std::uint8_t* data, std::uint64_t size,
                                   std::uint64_t pos) {
    if (pos + 3 > size) return 0;
    if (data[pos] != 0 || data[pos + 1] != 0) return 0;
    if (pos + 4 <= size && data[pos + 2] == 0 && data[pos + 3] == 1) return 4;
    if (data[pos + 2] == 1) return 3;
    return 0;
}

std::uint64_t find_start_code(const std::uint8_t* data, std::uint64_t size,
                              std::uint64_t from) {
    for (std::uint64_t i = from; i + 3 <= size; ++i) {
        if (data[i] != 0) continue;
        if (start_code_length_at(data, size, i) != 0) return i;
    }
    return size;
}

// Trims trailing zero padding from the payload region [begin, end).
std::uint64_t trim_trailing_zeros(const std::uint8_t* data, std::uint64_t begin,
                                  std::uint64_t end) {
    while (end > begin && data[end - 1] == 0) --end;
    return end;
}

void add_diagnostic(NalScanResult& result, ErrorKind kind, std::uint64_t offset,
                    std::string message) {
    Diagnostic d;
    d.kind = kind;
    d.offset = offset;
    d.message = std::move(message);
    result.diagnostics.push_back(std::move(d));
}

}  // namespace

NalScanResult scan_nal_units(const ByteSource& source) {
    NalScanResult result;
    const std::uint8_t* data = source.data();
    const std::uint64_t size = source.size();
    result.summary.total_bytes = size;
    if (size == 0) return result;

    const std::uint64_t first = find_start_code(data, size, 0);
    for (std::uint64_t i = 0; i < first; ++i) {
        if (data[i] == 0) {
            ++result.summary.leading_zero_bytes;
        } else {
            ++result.summary.leading_garbage_bytes;
        }
    }
    if (first > 0) {
        add_diagnostic(result, ErrorKind::StartCode, 0,
                       std::to_string(first) +
                           " byte(s) before the first Annex B start code were skipped");
    }

    std::uint64_t cursor = first;
    while (cursor < size) {
        const std::uint32_t sc_len = start_code_length_at(data, size, cursor);
        if (sc_len == 0) {
            add_diagnostic(result, ErrorKind::StartCode, cursor,
                           "unexpected bytes where a start code was expected");
            cursor = find_start_code(data, size, cursor + 1);
            continue;
        }
        const std::uint64_t header_offset = cursor + sc_len;
        if (header_offset >= size) {
            add_diagnostic(result, ErrorKind::Truncated, cursor,
                           "start code at end of file has no NAL header byte");
            ++result.summary.empty_nal_units;
            break;
        }

        // Search from the header byte: a code found there overlaps the byte we
        // would have used as a header, so no NAL exists between the two codes.
        const std::uint64_t next = find_start_code(data, size, header_offset);
        if (next == header_offset) {
            ++result.summary.consecutive_start_codes;
            add_diagnostic(result, ErrorKind::StartCode, next,
                           "consecutive start code with no NAL unit between codes");
            cursor = next;
            continue;
        }

        // `next` is either past the header byte or the end of the file.
        // Trailing zero bytes are permitted padding (trailing_zero_8bits); a
        // NAL cut short in the middle of its syntax is reported by the parser
        // that runs out of bits, at the exact syntax position.
        const std::uint64_t end = trim_trailing_zeros(data, header_offset + 1, next);
        const std::uint64_t padding = next - end;
        if (next == size) {
            result.summary.trailing_zero_bytes +=
                static_cast<std::uint32_t>(padding);
        } else {
            result.summary.inter_nal_zero_bytes +=
                static_cast<std::uint32_t>(padding);
        }

        NalUnit unit;
        unit.start_code_offset = cursor;
        unit.start_code_length = sc_len;
        unit.payload_offset = header_offset;
        unit.payload_length = end - header_offset - 1;
        unit.header = data[header_offset];

        const int type = unit.unit_type();
        if (unit.payload_length == 0) {
            ++result.summary.empty_nal_units;
            add_diagnostic(result, ErrorKind::Truncated, cursor,
                           std::string("NAL unit type ") + std::to_string(type) +
                               " (" + nal_unit_type_name(type) +
                               ") has an empty payload");
        }
        if (unit.forbidden_zero_bit()) {
            add_diagnostic(result, ErrorKind::IllegalValue, header_offset,
                           "forbidden_zero_bit is set in NAL header");
        }
        if (type == 0 || type == 31) {
            add_diagnostic(result, ErrorKind::Unsupported, header_offset,
                           std::string("unsupported NAL unit type ") +
                               std::to_string(type));
        }
        if (unit.payload_length > kMaxNalSize) {
            add_diagnostic(result, ErrorKind::LimitExceeded, cursor,
                           "NAL unit payload is " +
                               std::to_string(unit.payload_length) + " bytes, limit is " +
                               std::to_string(kMaxNalSize));
        }

        result.units.push_back(unit);
        cursor = next;
    }
    return result;
}

}  // namespace h264inspect