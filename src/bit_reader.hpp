#ifndef H264_BIT_READER_HPP
#define H264_BIT_READER_HPP

#include <cstddef>
#include <cstdint>
#include <stdexcept>
#include <string>
#include <vector>

namespace h264 {

// Raised on every malformed bitstream condition. byte_offset is the file
// offset of the NAL unit being parsed (not the exact bit position); callers
// attach it when constructing the reader.
class ParseError : public std::runtime_error {
public:
    ParseError(std::uint64_t byte_offset, const std::string& what)
        : std::runtime_error(what), byte_offset_(byte_offset) {}

    std::uint64_t byte_offset() const noexcept { return byte_offset_; }

private:
    std::uint64_t byte_offset_;
};

// Big-endian bit reader over an RBSP byte buffer with explicit bounds
// checking. Every read past the end of the buffer throws ParseError instead
// of silently returning zero, so truncated code words cannot be misparsed.
class BitReader {
public:
    BitReader(const std::vector<std::uint8_t>& data, std::uint64_t byte_offset)
        : data_(data.data()), size_(data.size()), byte_offset_(byte_offset) {}

    BitReader(const std::uint8_t* data, std::size_t size, std::uint64_t byte_offset)
        : data_(data), size_(size), byte_offset_(byte_offset) {}

    std::uint64_t byte_offset() const noexcept { return byte_offset_; }
    std::size_t bit_position() const noexcept { return pos_; }
    std::size_t bit_left() const noexcept { return size_ * 8u - pos_; }
    bool eof() const noexcept { return pos_ >= size_ * 8u; }
    bool byte_aligned() const noexcept { return (pos_ % 8u) == 0u; }

    // Single bit, value 0 or 1.
    std::uint32_t read_bit() {
        if (pos_ >= size_ * 8u) {
            throw ParseError(byte_offset_, "bit read past end of NAL unit");
        }
        const std::size_t byte_index = pos_ >> 3;
        const unsigned bit_index = 7u - static_cast<unsigned>(pos_ & 7u);
        ++pos_;
        return (data_[byte_index] >> bit_index) & 1u;
    }

    // 1..32 bit unsigned integer, MSB first.
    std::uint32_t read_bits(unsigned n) {
        if (n == 0u) return 0u;
        if (n > 32u) {
            throw ParseError(byte_offset_, "read_bits: width > 32");
        }
        if (static_cast<std::uint64_t>(pos_) + n > size_ * 8u) {
            throw ParseError(byte_offset_, "bit read past end of NAL unit");
        }
        std::uint32_t value = 0;
        for (unsigned i = 0; i < n; ++i) {
            const std::size_t byte_index = pos_ >> 3;
            const unsigned bit_index = 7u - static_cast<unsigned>(pos_ & 7u);
            ++pos_;
            value = (value << 1) | ((data_[byte_index] >> bit_index) & 1u);
        }
        return value;
    }

    // Exp-Golomb helpers need to know how many zero bits are available
    // without consuming past the buffer end.
    void align_to_byte() {
        while (!byte_aligned()) {
            read_bit();
        }
    }

private:
    const std::uint8_t* data_;
    std::size_t size_;
    std::size_t pos_ = 0;
    std::uint64_t byte_offset_;
};

}  // namespace h264

#endif
