#include "exp_golomb.hpp"

#include <algorithm>
#include <limits>

namespace h264inspect {

void ExpGolombStats::merge(const ExpGolombStats& other) {
    ue_calls += other.ue_calls;
    se_calls += other.se_calls;
    ue_failures += other.ue_failures;
    se_failures += other.se_failures;
    max_ue_bits = std::max(max_ue_bits, other.max_ue_bits);
    max_se_bits = std::max(max_se_bits, other.max_se_bits);
    max_leading_zeros = std::max(max_leading_zeros, other.max_leading_zeros);
    max_ue_value = std::max(max_ue_value, other.max_ue_value);
    max_se_magnitude = std::max(max_se_magnitude, other.max_se_magnitude);
}

bool ExpGolombDecoder::fail(ErrorKind kind, Diagnostic* diagnostic,
                            const char* message) {
    if (diagnostic) {
        diagnostic->kind = kind;
        diagnostic->offset = reader_.current_offset();
        diagnostic->message = message;
    }
    return false;
}

bool ExpGolombDecoder::read_code(std::uint64_t& code_num,
                                 std::uint32_t& bits_used,
                                 std::uint32_t& leading_zeros,
                                 Diagnostic* diagnostic) {
    leading_zeros = 0;
    bool bit = false;
    while (true) {
        if (!reader_.read_bit(bit)) {
            return fail(ErrorKind::Truncated, diagnostic,
                        "truncated Exp-Golomb codeword");
        }
        if (bit) break;
        ++leading_zeros;
        if (leading_zeros > limits_.max_leading_zeros) {
            return fail(ErrorKind::ExpGolomb, diagnostic,
                        "Exp-Golomb codeword exceeds leading-zero limit");
        }
    }
    const std::uint32_t total_bits = 2 * leading_zeros + 1;
    if (total_bits > limits_.max_codeword_bits) {
        return fail(ErrorKind::ExpGolomb, diagnostic,
                    "Exp-Golomb codeword exceeds width limit");
    }
    std::uint64_t suffix = 0;
    for (std::uint32_t i = 0; i < leading_zeros; ++i) {
        if (!reader_.read_bit(bit)) {
            return fail(ErrorKind::Truncated, diagnostic,
                        "truncated Exp-Golomb suffix");
        }
        suffix = (suffix << 1) | (bit ? 1u : 0u);
    }
    const std::uint64_t base = (std::uint64_t{1} << leading_zeros) - 1;
    code_num = base + suffix;
    bits_used = total_bits;
    return true;
}

bool ExpGolombDecoder::read_ue(std::uint64_t& value, Diagnostic* diagnostic) {
    ++stats_.ue_calls;
    std::uint64_t code_num = 0;
    std::uint32_t bits_used = 0;
    std::uint32_t leading_zeros = 0;
    if (!read_code(code_num, bits_used, leading_zeros, diagnostic)) {
        ++stats_.ue_failures;
        return false;
    }
    stats_.max_ue_bits = std::max(stats_.max_ue_bits, bits_used);
    stats_.max_leading_zeros = std::max(stats_.max_leading_zeros, leading_zeros);
    stats_.max_ue_value = std::max(stats_.max_ue_value, code_num);
    value = code_num;
    return true;
}

bool ExpGolombDecoder::read_se(std::int64_t& value, Diagnostic* diagnostic) {
    ++stats_.se_calls;
    std::uint64_t code_num = 0;
    std::uint32_t bits_used = 0;
    std::uint32_t leading_zeros = 0;
    if (!read_code(code_num, bits_used, leading_zeros, diagnostic)) {
        ++stats_.se_failures;
        return false;
    }
    stats_.max_se_bits = std::max(stats_.max_se_bits, bits_used);
    stats_.max_leading_zeros = std::max(stats_.max_leading_zeros, leading_zeros);
    if (code_num == std::numeric_limits<std::uint64_t>::max()) {
        ++stats_.se_failures;
        return fail(ErrorKind::ExpGolomb, diagnostic,
                    "signed Exp-Golomb value out of range");
    }
    const std::uint64_t magnitude = (code_num + 1) / 2;
    stats_.max_se_magnitude = std::max(stats_.max_se_magnitude, magnitude);
    if (magnitude > static_cast<std::uint64_t>(std::numeric_limits<std::int64_t>::max())) {
        ++stats_.se_failures;
        return fail(ErrorKind::ExpGolomb, diagnostic,
                    "signed Exp-Golomb value out of range");
    }
    value = (code_num & 1u) ? static_cast<std::int64_t>(magnitude)
                            : -static_cast<std::int64_t>(magnitude);
    return true;
}

bool ExpGolombDecoder::read_ue_u32(std::uint32_t& value, Diagnostic* diagnostic) {
    std::uint64_t raw = 0;
    if (!read_ue(raw, diagnostic)) return false;
    if (raw > std::numeric_limits<std::uint32_t>::max()) {
        ++stats_.ue_failures;
        if (diagnostic) {
            diagnostic->kind = ErrorKind::ExpGolomb;
            diagnostic->offset = reader_.current_offset();
            diagnostic->message = "Exp-Golomb value exceeds 32 bits";
        }
        return false;
    }
    value = static_cast<std::uint32_t>(raw);
    return true;
}

}  // namespace h264inspect
