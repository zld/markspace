#include "bit_reader.hpp"

namespace h264 {

std::vector<std::uint8_t> rbsp_unescape(const std::uint8_t* data,
                                        std::size_t size) {
    std::vector<std::uint8_t> rbsp;
    rbsp.reserve(size);
    std::size_t zero_run = 0;
    for (std::size_t i = 0; i < size; ++i) {
        const std::uint8_t byte = data[i];
        if (zero_run >= 2 && byte == 0x03) {
            // Emulation prevention byte: drop it and reset the run, because
            // the 0x03 itself does not count as a zero for future sequences.
            zero_run = 0;
            continue;
        }
        rbsp.push_back(byte);
        zero_run = (byte == 0x00) ? zero_run + 1 : 0;
    }
    return rbsp;
}

BitReader::BitReader(const uint8_t* data, std::size_t size)
    : data_(data), size_(size) {}

bool BitReader::read_bit(uint32_t& out) {
    if (bit_pos_ >= size_bits()) {
        return false;
    }
    const std::size_t byte_index = static_cast<std::size_t>(bit_pos_ >> 3);
    const unsigned shift = 7u - static_cast<unsigned>(bit_pos_ & 7u);
    out = (data_[byte_index] >> shift) & 1u;
    ++bit_pos_;
    return true;
}

bool BitReader::read_bits(unsigned n, uint32_t& out) {
    if (n > 32) {
        return false;
    }
    if (bits_remaining() < n) {
        return false;
    }
    uint32_t value = 0;
    for (unsigned i = 0; i < n; ++i) {
        uint32_t bit = 0;
        // bits_remaining() was checked up front, so this cannot fail.
        read_bit(bit);
        value = (value << 1) | bit;
    }
    out = value;
    return true;
}

bool BitReader::read_ue(uint32_t& out) {
    // Count leading zeros without consuming bits on failure.
    std::uint64_t scan = bit_pos_;
    unsigned leading_zeros = 0;
    while (true) {
        if (scan >= size_bits()) {
            return false;
        }
        const std::size_t byte_index = static_cast<std::size_t>(scan >> 3);
        const unsigned shift = 7u - static_cast<unsigned>(scan & 7u);
        const uint32_t bit = (data_[byte_index] >> shift) & 1u;
        if (bit != 0) {
            break;
        }
        ++leading_zeros;
        ++scan;
        if (leading_zeros >= 32) {
            return false;  // code length exceeds any representable value
        }
    }
    // `scan` points at the single terminating 1 bit.
    const unsigned suffix_bits = leading_zeros;
    if (suffix_bits == 0) {
        out = 0;
        bit_pos_ = scan + 1;
        return true;
    }
    if (suffix_bits > 24) {
        // 2^suffix is astronomically large; reject before allocating work.
        return false;
    }
    std::uint64_t code = 1u;
    for (unsigned i = 0; i < suffix_bits; ++i) {
        const std::uint64_t pos = scan + 1 + i;
        if (pos >= size_bits()) {
            return false;
        }
        const std::size_t byte_index = static_cast<std::size_t>(pos >> 3);
        const unsigned shift = 7u - static_cast<unsigned>(pos & 7u);
        code = (code << 1) | ((data_[byte_index] >> shift) & 1u);
    }
    const std::uint64_t value = code - 1u;
    if (value > kMaxExpGolombValue) {
        return false;
    }
    out = static_cast<uint32_t>(value);
    bit_pos_ = scan + 1 + suffix_bits;
    return true;
}

bool BitReader::read_se(int32_t& out) {
    uint32_t ue = 0;
    if (!read_ue(ue)) {
        return false;
    }
    const int64_t magnitude = static_cast<int64_t>((ue + 1u) >> 1);
    const int64_t value = (ue & 1u) ? magnitude : -magnitude;
    // kMaxExpGolombValue keeps this well inside int32_t.
    out = static_cast<int32_t>(value);
    return true;
}

bool BitReader::skip_bits(std::uint64_t n) {
    if (bits_remaining() < n) {
        return false;
    }
    bit_pos_ += n;
    return true;
}

std::uint64_t BitReader::bits_remaining() const {
    const std::uint64_t total = size_bits();
    return total > bit_pos_ ? total - bit_pos_ : 0;
}

}  // namespace h264
