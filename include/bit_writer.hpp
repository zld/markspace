// Minimal MSB-first bit writer. Used to generate sample streams and fixtures;
// it never participates in analysis.
#pragma once

#include <cstdint>
#include <vector>

namespace h264inspect {

class BitWriter {
public:
    void bit(bool value) {
        if (bit_position_ == 0) bytes_.push_back(0);
        if (value) bytes_.back() |= static_cast<std::uint8_t>(0x80 >> bit_position_);
        bit_position_ = (bit_position_ + 1) & 7;
    }

    void bits(std::uint64_t value, int count) {
        for (int i = count - 1; i >= 0; --i) {
            bit(((value >> static_cast<unsigned>(i)) & 1u) != 0);
        }
    }

    void ue(std::uint64_t value) {
        const std::uint64_t code = value + 1;
        int length = 0;
        for (std::uint64_t v = code; v > 1; v >>= 1) ++length;
        bits(0, length);
        for (int i = length; i >= 0; --i) {
            bit(((code >> static_cast<unsigned>(i)) & 1u) != 0);
        }
    }

    void se(std::int64_t value) {
        const std::uint64_t magnitude = value <= 0
                                            ? static_cast<std::uint64_t>(-value)
                                            : static_cast<std::uint64_t>(value);
        ue(value <= 0 ? magnitude * 2 : magnitude * 2 - 1);
    }

    void byte_align_zero() {
        while (bit_position_ != 0) bit(false);
    }

    void rbsp_trailing_bits() {
        bit(true);
        byte_align_zero();
    }

    const std::vector<std::uint8_t>& bytes() const { return bytes_; }

private:
    std::vector<std::uint8_t> bytes_;
    int bit_position_ = 0;
};

}  // namespace h264inspect
