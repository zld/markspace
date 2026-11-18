#include "byte_source.hpp"

#include <cerrno>
#include <cstring>

namespace h264inspect {

const char* error_kind_name(ErrorKind kind) {
    switch (kind) {
        case ErrorKind::None: return "none";
        case ErrorKind::StartCode: return "start_code";
        case ErrorKind::Truncated: return "truncated";
        case ErrorKind::ExpGolomb: return "exp_golomb";
        case ErrorKind::UnknownPps: return "unknown_pps";
        case ErrorKind::UnknownSps: return "unknown_sps";
        case ErrorKind::IllegalValue: return "illegal_value";
        case ErrorKind::LimitExceeded: return "limit_exceeded";
        case ErrorKind::Unsupported: return "unsupported";
        case ErrorKind::Io: return "io";
    }
    return "unknown";
}

bool ByteSource::open(const std::string& path, std::uint64_t max_size,
                      Diagnostic* error) {
    data_.clear();
    size_ = 0;
    auto fail = [&](ErrorKind kind, const std::string& message) {
        if (error) {
            error->kind = kind;
            error->offset = 0;
            error->message = message;
        }
        return false;
    };

    std::FILE* file = std::fopen(path.c_str(), "rb");
    if (file == nullptr) {
        return fail(ErrorKind::Io, "cannot open '" + path + "': " + std::strerror(errno));
    }
    struct Closer {
        std::FILE* f;
        ~Closer() { std::fclose(f); }
    } closer{file};

    if (std::fseek(file, 0, SEEK_END) != 0) {
        return fail(ErrorKind::Io, "cannot seek '" + path + "'");
    }
    long end = std::ftell(file);
    if (end < 0) {
        return fail(ErrorKind::Io, "cannot determine size of '" + path + "'");
    }
    if (static_cast<std::uint64_t>(end) > max_size) {
        return fail(ErrorKind::LimitExceeded,
                    "input is " + std::to_string(end) + " bytes, limit is " +
                        std::to_string(max_size));
    }
    if (std::fseek(file, 0, SEEK_SET) != 0) {
        return fail(ErrorKind::Io, "cannot rewind '" + path + "'");
    }

    const std::uint64_t total = static_cast<std::uint64_t>(end);
    try {
        data_.resize(static_cast<std::size_t>(total));
    } catch (const std::bad_alloc&) {
        return fail(ErrorKind::Io, "allocation failed for " + std::to_string(total) + " bytes");
    }
    if (total > 0) {
        std::size_t got = std::fread(data_.data(), 1, static_cast<std::size_t>(total), file);
        if (got != static_cast<std::size_t>(total)) {
            data_.clear();
            return fail(ErrorKind::Io, "short read on '" + path + "'");
        }
    }
    size_ = total;
    return true;
}

}  // namespace h264inspect
