// Bounded file reading with absolute offset tracking.
#pragma once

#include <cstddef>
#include <cstdint>
#include <cstdio>
#include <string>
#include <vector>

namespace h264inspect {

// Error categories reported by the analyzer. Codes are stable for JSON output.
enum class ErrorKind {
    None = 0,
    StartCode = 1,       // malformed Annex B start-code sequence
    Truncated = 2,       // NAL or codeword ends before the syntax is complete
    ExpGolomb = 3,       // invalid Exp-Golomb codeword (overlong / out of range)
    UnknownPps = 4,      // slice references a PPS that was never seen
    UnknownSps = 5,      // SPS id has no active definition
    IllegalValue = 6,    // value violates H.264 constraints (crop, dims, flags)
    LimitExceeded = 7,   // exceeds a configured hard limit
    Unsupported = 8,     // syntax extension outside analyzer scope
    Io = 9,              // file could not be read
};

const char* error_kind_name(ErrorKind kind);

struct Diagnostic {
    ErrorKind kind = ErrorKind::None;
    std::uint64_t offset = 0;  // absolute byte offset into the input file
    std::string message;
};

// Reads a whole regular file into memory with a hard size cap.
// The file is treated as opaque bytes; no text interpretation is performed.
class ByteSource {
public:
    static constexpr std::uint64_t kDefaultMaxSize = 256ull * 1024ull * 1024ull;

    ByteSource() = default;

    // Loads `path`. Returns false and fills `error` on failure.
    bool open(const std::string& path, std::uint64_t max_size, Diagnostic* error);

    const std::uint8_t* data() const { return data_.data(); }
    std::uint64_t size() const { return data_.size(); }
    bool empty() const { return data_.empty(); }

    // Copies [offset, offset+length) when fully in range.
    bool read(std::uint64_t offset, std::uint64_t length,
              std::vector<std::uint8_t>& out) const {
        if (offset > size_ || length > size_ - offset) return false;
        out.assign(data_.begin() + static_cast<std::size_t>(offset),
                   data_.begin() + static_cast<std::size_t>(offset + length));
        return true;
    }

private:
    std::vector<std::uint8_t> data_;
    std::uint64_t size_ = 0;
};

}  // namespace h264inspect
