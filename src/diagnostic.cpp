#include "diagnostic.hpp"

namespace h264 {

void DiagnosticSink::add(std::uint64_t offset, const char* severity,
                         const char* code, std::string message) {
    if (std::string(severity) == kSeverityError) {
        ++error_count_;
    } else {
        ++warning_count_;
    }
    if (items_.size() >= kMaxStored) {
        ++dropped_;
        return;
    }
    Diagnostic d;
    d.offset = offset;
    d.severity = severity;
    d.code = code;
    d.message = std::move(message);
    items_.push_back(std::move(d));
}

}  // namespace h264
