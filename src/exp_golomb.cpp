#include "exp_golomb.hpp"

namespace h264 {

std::uint32_t decodeUe(BitReader& reader, int maxLeadingZeros) {
    int leadingZeros = 0;
    while (true) {
        if (leadingZeros > maxLeadingZeros) {
            reader.setError();
            return 0;
        }
        if (reader.bitsRemaining() == 0) {
            reader.setError();
            return 0;
        }
        std::uint32_t bit = reader.readBit();
        if (bit != 0) break;
        ++leadingZeros;
    }
    // leadingZeros == 0 => codeNum 0
    std::uint32_t suffix = 0;
    if (leadingZeros > 0) {
        if (reader.bitsRemaining() < static_cast<std::size_t>(leadingZeros)) {
            reader.setError();
            return 0;
        }
        suffix = reader.readBits(leadingZeros);
    }
    return (static_cast<std::uint32_t>(1) << leadingZeros) - 1 + suffix;
}

std::int32_t decodeSe(BitReader& reader, int maxLeadingZeros) {
    std::uint32_t codeNum = decodeUe(reader, maxLeadingZeros);
    if (reader.error()) return 0;
    if (codeNum & 1u) {
        return static_cast<std::int32_t>((codeNum + 1) / 2);
    }
    return -static_cast<std::int32_t>(codeNum / 2);
}

} // namespace h264
