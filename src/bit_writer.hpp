#ifndef H264_BIT_WRITER_HPP
#define H264_BIT_WRITER_HPP

#include <cstdint>
#include <stdexcept>
#include <string>
#include <vector>

namespace h264 {

// Big-endian bit writer used only by the deterministic fixture generator.
// Mirrors BitReader's bit ordering so the two can round-trip every syntax
// element. Bytes are finalized with H.264 RBSP trailing bits.
class BitWriter {
public:
    void write_bit(unsigned b) {
        if (b) {
            if (byte_count_ >= buf_.size()) buf_.push_back(0);
            buf_[byte_count_] |= static_cast<std::uint8_t>(
                1u << (7u - bit_in_byte_));
        } else {
            if (byte_count_ >= buf_.size()) buf_.push_back(0);
        }
        if (++bit_in_byte_ == 8u) {
            bit_in_byte_ = 0;
            ++byte_count_;
        }
    }

    void write_bits(std::uint32_t value, unsigned n) {
        if (n > 32u) {
            throw std::invalid_argument("write_bits: width > 32");
        }
        for (int i = static_cast<int>(n) - 1; i >= 0; --i) {
            write_bit((value >> i) & 1u);
        }
    }

    // unsigned Exp-Golomb (ue(v)).
    void write_ue(std::uint32_t code_num) {
        const std::uint64_t mapped = static_cast<std::uint64_t>(code_num) + 1ull;
        unsigned leading = 0;
        std::uint64_t v = mapped;
        while (v > 1u) { v >>= 1; ++leading; }
        for (unsigned i = 0; i < leading; ++i) write_bit(0);
        write_bits(static_cast<std::uint32_t>(mapped), leading + 1u);
    }

    // signed Exp-Golomb (se(v)).
    void write_se(std::int32_t value) {
        if (value > 0) {
            write_ue(static_cast<std::uint32_t>(2 * value - 1));
        } else if (value < 0) {
            write_ue(static_cast<std::uint32_t>(-2 * value));
        } else {
            write_ue(0);
        }
    }

    // RBSP stop bit plus alignment zeros (7.3.2.11).
    void rbsp_trailing_bits() {
        write_bit(1);
        while (bit_in_byte_ != 0u) write_bit(0);
    }

    bool byte_aligned() const noexcept { return bit_in_byte_ == 0u; }
    std::size_t size_bytes() const noexcept { return buf_.size(); }

    const std::vector<std::uint8_t>& bytes() const noexcept { return buf_; }
    std::vector<std::uint8_t>&& take() { return std::move(buf_); }

private:
    std::vector<std::uint8_t> buf_;
    std::size_t byte_count_ = 0;
    unsigned bit_in_byte_ = 0;
};

// Insert emulation prevention bytes (0x03) so that no byte sequence of
// 00 00 00 / 00 00 01 / 00 00 02 / 00 00 03 survives in the EBSP. The
// scanner's removal logic must invert this exactly.
inline std::vector<std::uint8_t> escape_ebsp(
        const std::vector<std::uint8_t>& rbsp) {
    std::vector<std::uint8_t> out;
    out.reserve(rbsp.size() + rbsp.size() / 32u + 1u);
    unsigned zeros = 0;
    for (std::uint8_t b : rbsp) {
        if (zeros >= 2u && b <= 3u) {
            out.push_back(0x03);
            zeros = 0;
        }
        out.push_back(b);
        zeros = (b == 0u) ? zeros + 1u : 0u;
    }
    return out;
}

}  // namespace h264

#endif
