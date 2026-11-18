#include "bit_reader.hpp"

#include <algorithm>

namespace h264inspect {

std::uint64_t RbspBuffer::source_offset_of(std::size_t index) const {
    // Each dropped byte shifts later RBSP indices one source byte earlier.
    const auto after = std::upper_bound(removed_at.begin(), removed_at.end(),
                                        static_cast<std::uint32_t>(
                                            std::min<std::size_t>(index, 0xffffffffull)));
    const std::size_t dropped = static_cast<std::size_t>(after - removed_at.begin());
    return source_offset + static_cast<std::uint64_t>(index) + dropped;
}

bool build_rbsp(const std::uint8_t* payload, std::size_t size,
                std::uint64_t source_offset, RbspBuffer& out,
                Diagnostic* diagnostic) {
    out.bytes.clear();
    out.removed_at.clear();
    out.source_offset = source_offset;
    out.removed_count = 0;

    try {
        out.bytes.reserve(size);
    } catch (const std::bad_alloc&) {
        if (diagnostic) {
            diagnostic->kind = ErrorKind::Io;
            diagnostic->offset = source_offset;
            diagnostic->message = "allocation failed while building RBSP";
        }
        return false;
    }

    std::size_t zeros = 0;
    for (std::size_t i = 0; i < size; ++i) {
        const std::uint8_t byte = payload[i];
        if (zeros >= 2 && byte == 0x03) {
            // Emulation-prevention byte: drop it. In H.264 it must be followed
            // by 0x00..0x03, but tolerate other values and keep decoding.
            out.removed_at.push_back(static_cast<std::uint32_t>(out.bytes.size()));
            ++out.removed_count;
            zeros = 0;
            continue;
        }
        out.bytes.push_back(byte);
        zeros = (byte == 0x00) ? zeros + 1 : 0;
    }

    if (zeros >= 2) {
        // Trailing zero bytes were expected to be stripped by the NAL scanner.
        if (diagnostic) {
            diagnostic->kind = ErrorKind::Truncated;
            diagnostic->offset = out.source_offset_of(out.bytes.size() - zeros);
            diagnostic->message = "NAL payload ends with unescaped zero bytes";
        }
    }
    return true;
}

BitReader::BitReader(const RbspBuffer& rbsp) : rbsp_(rbsp) {}

bool BitReader::read_bit(bool& out) {
    if (bit_position_ >= rbsp_.bytes.size() * 8) return false;
    const std::size_t index = bit_position_ >> 3;
    const int shift = 7 - static_cast<int>(bit_position_ & 7u);
    out = ((rbsp_.bytes[index] >> shift) & 1u) != 0;
    ++bit_position_;
    return true;
}

bool BitReader::read_bits(int count, std::uint32_t& out) {
    if (count < 0 || count > 32) return false;
    if (static_cast<std::size_t>(count) > bits_remaining()) return false;
    std::uint32_t value = 0;
    for (int i = 0; i < count; ++i) {
        bool bit = false;
        if (!read_bit(bit)) return false;
        value = (value << 1) | (bit ? 1u : 0u);
    }
    out = value;
    return true;
}

bool BitReader::skip_bits(int count) {
    if (count < 0) return false;
    if (static_cast<std::size_t>(count) > bits_remaining()) return false;
    bit_position_ += static_cast<std::size_t>(count);
    return true;
}

bool BitReader::read_byte(std::uint8_t& out) {
    if (!byte_aligned() || bit_position_ + 8 > rbsp_.bytes.size() * 8) return false;
    out = rbsp_.bytes[bit_position_ >> 3];
    bit_position_ += 8;
    return true;
}

std::size_t BitReader::bits_remaining() const {
    const std::size_t total = rbsp_.bytes.size() * 8;
    return bit_position_ >= total ? 0 : total - bit_position_;
}

bool BitReader::more_rbsp_data() const {
    const std::size_t total_bits = rbsp_.bytes.size() * 8;
    for (std::size_t pos = total_bits; pos > bit_position_; --pos) {
        const std::size_t index = (pos - 1) >> 3;
        const int shift = 7 - static_cast<int>((pos - 1) & 7u);
        if (((rbsp_.bytes[index] >> shift) & 1u) != 0) {
            // A final set bit is the rbsp_stop_one_bit; anything before it is data.
            return (pos - 1) > bit_position_;
        }
    }
    return false;
}

bool BitReader::read_rbsp_trailing_bits(Diagnostic* diagnostic) {
    bool stop = false;
    if (!read_bit(stop) || !stop) {
        if (diagnostic) {
            diagnostic->kind = ErrorKind::Truncated;
            diagnostic->offset = current_offset();
            diagnostic->message = "missing rbsp_stop_one_bit";
        }
        return false;
    }
    const int alignment = static_cast<int>((8 - (bit_position_ & 7u)) & 7u);
    std::uint32_t pad = 0;
    if (alignment > 0 && !read_bits(alignment, pad)) {
        if (diagnostic) {
            diagnostic->kind = ErrorKind::Truncated;
            diagnostic->offset = current_offset();
            diagnostic->message = "truncated rbsp alignment bits";
        }
        return false;
    }
    if (pad != 0) {
        if (diagnostic) {
            diagnostic->kind = ErrorKind::IllegalValue;
            diagnostic->offset = current_offset();
            diagnostic->message = "non-zero rbsp alignment bits";
        }
        return false;
    }
    if (more_rbsp_data()) {
        if (diagnostic) {
            diagnostic->kind = ErrorKind::IllegalValue;
            diagnostic->offset = current_offset();
            diagnostic->message = "trailing data after rbsp_trailing_bits";
        }
        return false;
    }
    return true;
}

std::uint64_t BitReader::current_offset() const {
    return rbsp_.source_offset_of(bit_position_ >> 3);
}

}  // namespace h264inspect
