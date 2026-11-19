#include "bit_writer.hpp"

namespace h264::gen {

void BitWriter::write_bits(std::uint32_t value, unsigned n) {
    for (unsigned i = n; i > 0; --i) {
        const unsigned bit = (value >> (i - 1)) & 1u;
        if (bit_count_ == 0) data_.push_back(0);
        data_.back() = static_cast<std::uint8_t>(
            data_.back() | (static_cast<std::uint8_t>(bit)
                            << (7 - bit_count_)));
        bit_count_ = (bit_count_ + 1) & 7u;
    }
}

void BitWriter::write_ue(std::uint32_t value) {
    const std::uint64_t code_num = static_cast<std::uint64_t>(value) + 1;
    unsigned bits = 0;
    std::uint64_t probe = code_num;
    while (probe != 0) {
        ++bits;
        probe >>= 1;
    }
    // codeNum maps to (bits-1) leading zeros followed by the binary code.
    for (unsigned i = 0; i + 1 < bits; ++i) write_bits(0, 1);
    write_bits(static_cast<std::uint32_t>(code_num), bits);
}

void BitWriter::write_se(std::int32_t value) {
    std::uint32_t code;
    if (value > 0) {
        code = static_cast<std::uint32_t>(2 * static_cast<std::int64_t>(value) - 1);
    } else {
        code = static_cast<std::uint32_t>(-2 * static_cast<std::int64_t>(value));
    }
    write_ue(code);
}

void BitWriter::align_zero() {
    if (bit_count_ != 0) {
        write_bits(0, 8 - bit_count_);
    }
}

std::vector<std::uint8_t> BitWriter::take_escaped() {
    std::vector<std::uint8_t> out;
    out.reserve(data_.size() + data_.size() / 16);
    unsigned zero_run = 0;
    for (std::uint8_t byte : data_) {
        if (zero_run >= 2 && byte <= 0x03) {
            out.push_back(0x03);
            zero_run = 0;
        }
        out.push_back(byte);
        zero_run = (byte == 0x00) ? zero_run + 1 : 0;
    }
    return out;
}

void append_nal(std::vector<std::uint8_t>& out, std::uint8_t nal_header,
                const std::vector<std::uint8_t>& rbsp, bool four_byte) {
    if (four_byte) {
        out.insert(out.end(), {0x00, 0x00, 0x00, 0x01});
    } else {
        out.insert(out.end(), {0x00, 0x00, 0x01});
    }
    out.push_back(nal_header);

    // Escape the RBSP into NAL-unit payload bytes.
    unsigned zero_run = 0;
    for (std::uint8_t byte : rbsp) {
        if (zero_run >= 2 && byte <= 0x03) {
            out.push_back(0x03);
            zero_run = 0;
        }
        out.push_back(byte);
        zero_run = (byte == 0x00) ? zero_run + 1 : 0;
    }
}

}  // namespace h264::gen
