// Bit-level reading over an RBSP buffer with emulation-prevention removal.
#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

#include "byte_source.hpp"

namespace h264inspect {

// RBSP bytes derived from a NAL payload, plus the source-offset bookkeeping
// needed to map a RBSP byte index back to an offset in the input file.
struct RbspBuffer {
    std::vector<std::uint8_t> bytes;
    // RBSP byte indices at which an emulation-prevention byte was dropped.
    std::vector<std::uint32_t> removed_at;
    std::uint64_t source_offset = 0;  // file offset of the first payload byte
    std::uint32_t removed_count = 0;

    // File offset of RBSP byte `index`; clamped to the source range.
    std::uint64_t source_offset_of(std::size_t index) const;
};

// Removes 0x03 emulation-prevention bytes from an EBSP NAL payload.
// A 0x00 0x00 0x03 sequence drops the 0x03 byte. A dangling 0x00 0x00 at
// the very end is recorded as truncation but the bytes are still emitted.
bool build_rbsp(const std::uint8_t* payload, std::size_t size,
                std::uint64_t source_offset, RbspBuffer& out,
                Diagnostic* diagnostic);

class BitReader {
public:
    explicit BitReader(const RbspBuffer& rbsp);

    // All read functions return false on end-of-data without modifying `out`.
    bool read_bit(bool& out);
    bool read_bits(int count, std::uint32_t& out);  // count in [0, 32]
    bool skip_bits(int count);
    bool read_byte(std::uint8_t& out);

    std::size_t bit_position() const { return bit_position_; }
    std::size_t bits_remaining() const;
    bool byte_aligned() const { return (bit_position_ & 7u) == 0; }
    bool exhausted() const { return bits_remaining() == 0; }

    // True when at least one more syntax bit follows the current position.
    // Used to decide whether a NAL payload ends in only trailing bits.
    bool more_rbsp_data() const;

    // Consumes rbsp_trailing_bits (a stop bit then zero alignment bits).
    bool read_rbsp_trailing_bits(Diagnostic* diagnostic);

    // Absolute file offset of the byte containing the current bit position.
    std::uint64_t current_offset() const;

private:
    const RbspBuffer& rbsp_;
    std::size_t bit_position_ = 0;
};

}  // namespace h264inspect
