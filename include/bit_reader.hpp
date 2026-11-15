#ifndef H264_BIT_READER_HPP
#define H264_BIT_READER_HPP

#include <cstddef>
#include <cstdint>
#include <vector>

namespace h264 {

// Bounded bit reader over an immutable byte buffer. All reads are range
// checked: reading past the end sets the error flag and returns zero.
class BitReader {
public:
    BitReader() = default;
    BitReader(const std::uint8_t* data, std::size_t size);

    // Reads n bits (1..32) in MSB-first order. Returns 0 and flags an error
    // on truncation or an invalid width.
    std::uint32_t readBits(int n);

    // Convenience single-bit / byte-aligned reads.
    std::uint32_t readBit();
    bool readFlag();
    std::uint32_t readUe(int maxLeadingZeros = 31);
    std::int32_t readSe(int maxLeadingZeros = 31);

    // Unsigned fixed-width integer of n bits (thin wrapper around readBits).
    std::uint32_t readU(int n) { return readBits(n); }

    void byteAlign();

    std::size_t bitPosition() const { return pos_; }
    std::size_t bytePosition() const { return pos_ / 8; }
    bool atEnd() const { return pos_ >= size_ * 8; }
    bool error() const { return error_; }
    void setError() { error_ = true; }

    // Number of bits still available, including the partially consumed byte.
    std::size_t bitsRemaining() const;

    // Number of payload bits excluding the trailing rbsp_stop_one_bit and
    // its alignment zeros. Returns 0 for an empty buffer.
    static std::size_t rbspDataBits(const std::uint8_t* data,
                                    std::size_t size);

private:
    const std::uint8_t* data_ = nullptr;
    std::size_t size_ = 0;
    std::size_t pos_ = 0;   // bit position
    bool error_ = false;
};

// Removes H.264 RBSP emulation prevention bytes (0x03 after 0x00 0x00).
// Returns the RBSP payload. A trailing 0x00 0x00 0x03 with no following byte
// is reported as truncated via the output flag.
std::vector<std::uint8_t> rbspUnescape(const std::uint8_t* data,
                                       std::size_t size,
                                       bool* truncated = nullptr);

} // namespace h264

#endif
