#include "bit_reader.hpp"

#include <cstring>

namespace h264 {

BitReader::BitReader(const std::uint8_t* data, std::size_t size)
    : data_(data), size_(size) {}

std::uint32_t BitReader::readBits(int n) {
    if (n <= 0 || n > 32) {
        error_ = true;
        return 0;
    }
    if (pos_ + static_cast<std::size_t>(n) > size_ * 8) {
        error_ = true;
        return 0;
    }
    std::uint32_t value = 0;
    for (int i = 0; i < n; ++i) {
        std::size_t bitIndex = pos_ + static_cast<std::size_t>(i);
        std::uint8_t byte = data_[bitIndex / 8];
        int bit = (byte >> (7 - static_cast<int>(bitIndex % 8))) & 1u;
        value = (value << 1) | static_cast<std::uint32_t>(bit);
    }
    pos_ += static_cast<std::size_t>(n);
    return value;
}

std::uint32_t BitReader::readBit() { return readBits(1); }

bool BitReader::readFlag() { return readBits(1) != 0; }

void BitReader::byteAlign() {
    std::size_t rem = pos_ % 8;
    if (rem != 0) pos_ += 8 - rem;
}

std::size_t BitReader::bitsRemaining() const {
    if (error_ || pos_ >= size_ * 8) return 0;
    return size_ * 8 - pos_;
}

std::size_t BitReader::rbspDataBits(const std::uint8_t* data,
                                    std::size_t size) {
    if (size == 0) return 0;
    // Locate the last set bit: the rbsp_stop_one_bit. Everything after it
    // is alignment padding and must not be treated as syntax.
    std::size_t total = size * 8;
    for (std::size_t bit = total; bit > 0; --bit) {
        std::size_t idx = bit - 1;
        if ((data[idx / 8] >> (7 - (idx % 8))) & 1u)
            return idx; // bits [0, idx) are payload
    }
    return 0;
}

std::vector<std::uint8_t> rbspUnescape(const std::uint8_t* data,
                                       std::size_t size,
                                       bool* truncated) {
    std::vector<std::uint8_t> rbsp;
    rbsp.reserve(size);
    if (truncated) *truncated = false;

    int zeroRun = 0;
    for (std::size_t i = 0; i < size; ++i) {
        std::uint8_t b = data[i];
        if (zeroRun >= 2 && b == 0x03) {
            // Emulation prevention byte; per spec a following byte must
            // exist. A trailing 00 00 03 means the RBSP is truncated.
            if (i + 1 >= size) {
                if (truncated) *truncated = true;
            }
            zeroRun = 0;
            continue;
        }
        rbsp.push_back(b);
        zeroRun = (b == 0x00) ? zeroRun + 1 : 0;
    }
    return rbsp;
}

} // namespace h264
