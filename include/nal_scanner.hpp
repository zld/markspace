#ifndef H264_NAL_SCANNER_HPP
#define H264_NAL_SCANNER_HPP

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

namespace h264 {

// H.264 NAL unit types (subset of ITU-T H.264 Table 7-1).
enum NalType : std::uint8_t {
    NAL_SLICE        = 1,
    NAL_DPA          = 2,
    NAL_DPB          = 3,
    NAL_DPC          = 4,
    NAL_IDR_SLICE    = 5,
    NAL_SEI          = 6,
    NAL_SPS          = 7,
    NAL_PPS          = 8,
    NAL_AUD          = 9,
    NAL_END_SEQ      = 10,
    NAL_END_STREAM   = 11,
    NAL_FILLER       = 12,
};

struct NalUnit {
    std::size_t offset = 0;       // byte offset of NAL header (payload start)
    std::size_t startCodeLen = 0; // 3/4 for Annex B, 0 for length-prefixed
    std::size_t size = 0;         // payload size including the header byte
    std::uint8_t header = 0;      // raw nal_unit() first byte
    std::uint8_t nalRefIdc = 0;
    std::uint8_t nalType = 0;
};

enum class StreamFormat { Unknown, AnnexB, LengthPrefixed };

struct ScanError {
    std::size_t offset = 0;
    std::string reason;
};

struct ScanResult {
    StreamFormat format = StreamFormat::Unknown;
    std::vector<NalUnit> nals;
    std::vector<ScanError> errors;
};

// Limits enforced while scanning (see project requirements).
constexpr std::size_t MAX_FILE_SIZE = 512ull * 1024 * 1024;
constexpr std::size_t MAX_NAL_SIZE  = 64ull * 1024 * 1024;

// Scan an in-memory buffer. formatHint: "auto", "annexb", "avcc".
// In tolerant mode malformed units are recorded in errors and skipped.
ScanResult scanNals(const std::uint8_t* data, std::size_t size,
                    const std::string& formatHint, bool tolerant);

const char* formatName(StreamFormat f);

} // namespace h264

#endif
