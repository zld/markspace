#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

namespace h264 {

// Removes emulation_prevention_three_byte bytes (0x03 following 00 00) from
// a NAL unit payload and returns the raw RBSP.  The output is never larger
// than the input, so allocation is bounded by the caller's buffer size.
std::vector<std::uint8_t> rbsp_unescape(const std::uint8_t* data,
                                        std::size_t size);

// Bounded bit reader over an RBSP byte buffer.
//
// Every read method returns false when the requested bits would run past the
// end of the buffer or when a decoded Exp-Golomb code exceeds the configured
// limit.  Once a read fails the reader is left untouched and the caller can
// still query bit_position() to build a diagnostic.  No method allocates.
class BitReader {
public:
    // Maximum magnitude accepted for a ue(v)/se(v) value.  H.264 syntax
    // elements used by this tool comfortably fit in this range; larger codes
    // are treated as out-of-range instead of being decoded further.
    static constexpr uint32_t kMaxExpGolombValue = 1u << 24;

    BitReader(const uint8_t* data, std::size_t size);

    // Reads one bit.  `out` is 0 or 1.
    bool read_bit(uint32_t& out);
    // Reads `n` bits, most significant bit first.  `n` must be <= 32.
    bool read_bits(unsigned n, uint32_t& out);
    // Unsigned exponential-Golomb, ue(v).
    bool read_ue(uint32_t& out);
    // Signed exponential-Golomb, se(v).
    bool read_se(int32_t& out);
    // Skips `n` bits.
    bool skip_bits(std::uint64_t n);

    // More bits available without a read failure.
    std::uint64_t bits_remaining() const;
    // Absolute bit index of the next bit to be read.
    std::uint64_t bit_position() const { return bit_pos_; }
    // Byte index of the next bit to be read (floor of bit_position()/8).
    std::uint64_t byte_position() const { return bit_pos_ / 8; }
    std::uint64_t size_bits() const { return static_cast<std::uint64_t>(size_) * 8u; }
    std::size_t size_bytes() const { return size_; }

private:
    const uint8_t* data_;
    std::size_t size_;
    std::uint64_t bit_pos_ = 0;
};

}  // namespace h264
