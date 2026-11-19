#include "media_types.hpp"

namespace pwsync {

const char* toString(MediaType type) {
    switch (type) {
        case MediaType::Video:
            return "video";
        case MediaType::Audio:
            return "audio";
        case MediaType::Other:
            return "other";
    }
    return "other";
}

const char* toString(ErrorCategory category) {
    switch (category) {
        case ErrorCategory::OpenFailed:
            return "open_failed";
        case ErrorCategory::MissingStream:
            return "missing_stream";
        case ErrorCategory::CorruptPacket:
            return "corrupt_packet";
        case ErrorCategory::DecodeError:
            return "decode_error";
        case ErrorCategory::LimitExceeded:
            return "limit_exceeded";
        case ErrorCategory::WriteFailed:
            return "write_failed";
        case ErrorCategory::BadParameter:
            return "bad_parameter";
    }
    return "unknown";
}

}  // namespace pwsync
