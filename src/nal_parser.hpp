#ifndef H264_NAL_PARSER_HPP
#define H264_NAL_PARSER_HPP

#include <cstdint>
#include <string>
#include <vector>

#include "diagnostics.hpp"

namespace h264 {

// ITU-T H.264 Table 7-1 NAL unit type codes.
enum NalType : std::uint8_t {
    NAL_SLICE = 1,
    NAL_DPA = 2,
    NAL_DPB = 3,
    NAL_DPC = 4,
    NAL_IDR = 5,
    NAL_SEI = 6,
    NAL_SPS = 7,
    NAL_PPS = 8,
    NAL_AUD = 9,
    NAL_END_SEQ = 10,
    NAL_END_STREAM = 11,
    NAL_FILLER = 12,
    NAL_SPS_EXT = 13,
};

const char* nal_type_name(unsigned nal_type);

// One parsed NAL unit. `payload` holds the RBSP with emulation prevention
// bytes (0x03 after 00 00) already removed, so syntax parsers never see them.
struct NalUnit {
    std::uint64_t start_offset = 0;   // absolute file offset of the start code
    std::uint64_t nal_offset = 0;     // absolute file offset of the header byte
    std::uint64_t raw_size = 0;       // NAL bytes excluding the start code
    unsigned nal_ref_idc = 0;
    unsigned nal_type = 0;
    bool valid = true;                // false when a structural error was recorded
    bool size_exceeded = false;       // payload hit max_nal_bytes
    std::vector<std::uint8_t> payload;  // header byte + RBSP, bounded in size
};

// Streaming Annex B scanner. Feed file chunks of any size; the scanner keeps
// only byte-sized state plus the single NAL in flight, so memory does not
// grow with file length.
//
// Zero bytes inside a NAL are held back ("pending") until the following byte
// decides their fate: 0x01 means they start the next start code (discard),
// 0x03 means emulation prevention (keep the zeros, drop the 0x03), anything
// else means ordinary payload (flush zeros then the byte).
class NalScanner {
public:
    static constexpr std::uint64_t kDefaultMaxNalBytes = 16ull * 1024ull * 1024ull;

    explicit NalScanner(Diagnostics& diag,
                        std::uint64_t max_nal_bytes = kDefaultMaxNalBytes)
        : diag_(diag), max_nal_bytes_(max_nal_bytes) {}

    // Feed the next file chunk; completed NALs are appended to out.
    void feed(const std::uint8_t* data, std::size_t length,
              std::vector<NalUnit>& out) {
        for (std::size_t i = 0; i < length; ++i) {
            on_byte(data[i], total_bytes_, out);
            ++total_bytes_;
        }
    }

    // Flush state at EOF: emits the final NAL and reports a file with no
    // start code or a truncated trailing NAL.
    void finish(std::vector<NalUnit>& out) {
        if (state_ == IDLE) {
            diag_.add(0, "no Annex B start code found in input");
            return;
        }
        if (state_ == AWAIT_HEADER) {
            diag_.add(pending_start_,
                      "truncated or empty NAL unit: no header byte");
            return;
        }
        // Pending zeros at EOF are RBSP trailing bits: keep them.
        flush_pending_zeros();
        finalize_nal(total_bytes_, out);
    }

    std::uint64_t total_bytes() const noexcept { return total_bytes_; }

private:
    enum State { IDLE, AWAIT_HEADER, IN_NAL };

    void on_byte(std::uint8_t b, std::uint64_t off,
                 std::vector<NalUnit>& out) {
        if (b == 0u) {
            if (zeros_ < 255u) ++zeros_;  // saturation keeps the counter safe
            return;
        }

        if (b == 1u && zeros_ >= 2u) {
            const std::uint64_t start_code_off = off - zeros_;
            if (state_ == IN_NAL) {
                // Pending zeros belong to this start code, not the RBSP.
                finalize_nal(start_code_off, out);
            } else if (state_ == AWAIT_HEADER) {
                diag_.add(pending_start_,
                          "empty NAL unit between start codes");
            }
            begin_await(start_code_off);
            return;
        }

        if (state_ == AWAIT_HEADER) {
            start_nal(off, b);  // first non-start-code byte is the header
            zeros_ = 0;
            return;
        }

        if (state_ == IDLE) {
            diag_.add(off,
                      "byte 0x" + to_hex(b) +
                      " outside any NAL unit (missing leading start code)");
            zeros_ = 0;
            return;
        }

        // IN_NAL, ordinary non-zero payload byte.
        if (b == 3u && zeros_ >= 2u) {
            // Emulation prevention: keep pending zeros, drop this 0x03.
            flush_pending_zeros();
            zeros_ = 0;
            return;
        }
        flush_pending_zeros();
        append_payload(b);
        zeros_ = 0;
    }

    static std::string to_hex(std::uint8_t b) {
        static const char* kHex = "0123456789abcdef";
        return std::string{ kHex[b >> 4], kHex[b & 0xFu] };
    }

    void flush_pending_zeros() {
        while (zeros_ > 0u) {
            append_payload(0);
            --zeros_;
        }
    }

    void begin_await(std::uint64_t start_code_off) {
        state_ = AWAIT_HEADER;
        pending_start_ = start_code_off;
        zeros_ = 0;
        size_exceeded_ = false;
        payload_.clear();
    }

    void start_nal(std::uint64_t header_off, std::uint8_t header) {
        if ((header & 0x80u) != 0u) {
            diag_.add(header_off, "forbidden_zero_bit set in NAL header");
        }
        state_ = IN_NAL;
        nal_start_ = pending_start_;
        nal_header_offset_ = header_off;
        payload_.push_back(header);
    }

    void append_payload(std::uint8_t b) {
        if (payload_.size() >= max_nal_bytes_) {
            if (!size_exceeded_) {
                size_exceeded_ = true;
                diag_.add(nal_header_offset_,
                          "NAL unit exceeds --max-nal-bytes limit of " +
                              std::to_string(max_nal_bytes_) +
                              " bytes; further payload discarded");
            }
            return;
        }
        payload_.push_back(b);
    }

    void finalize_nal(std::uint64_t end_off, std::vector<NalUnit>& out) {
        NalUnit nal;
        nal.start_offset = nal_start_;
        nal.nal_offset = nal_header_offset_;
        // Offsets are monotonic file positions, so this subtraction is the
        // exact NAL length and cannot overflow.
        nal.raw_size = end_off - nal_header_offset_;
        if (payload_.empty()) {
            diag_.add(nal_start_, "empty NAL unit: no header byte");
            nal.valid = false;
            out.push_back(std::move(nal));
            state_ = IDLE;
            return;
        }
        const std::uint8_t header = payload_[0];
        nal.nal_ref_idc = (header >> 5) & 0x3u;
        nal.nal_type = header & 0x1Fu;
        nal.size_exceeded = size_exceeded_;
        nal.valid = ((header & 0x80u) == 0u) && !size_exceeded_;
        nal.payload = std::move(payload_);
        out.push_back(std::move(nal));
        state_ = IDLE;
        payload_.clear();
    }

    Diagnostics& diag_;
    std::uint64_t max_nal_bytes_;
    std::uint64_t total_bytes_ = 0;

    State state_ = IDLE;
    unsigned zeros_ = 0;
    std::uint64_t pending_start_ = 0;
    std::uint64_t nal_start_ = 0;
    std::uint64_t nal_header_offset_ = 0;
    bool size_exceeded_ = false;
    std::vector<std::uint8_t> payload_;
};

inline const char* nal_type_name(unsigned nal_type) {
    switch (nal_type) {
        case 1: return "SLICE";
        case 2: return "DPB";
        case 3: return "DPA";
        case 4: return "DPC";
        case 5: return "IDR";
        case 6: return "SEI";
        case 7: return "SPS";
        case 8: return "PPS";
        case 9: return "AUD";
        case 10: return "END_SEQ";
        case 11: return "END_STREAM";
        case 12: return "FILLER";
        case 13: return "SPS_EXT";
        default: return "UNKNOWN";
    }
}

}  // namespace h264

#endif
