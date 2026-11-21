#include "timing.h"

#include <cctype>
#include <limits>

namespace {

bool parse_milliseconds(const std::string& value, long long& out) {
    if (value.empty() || value.size() > 18) {
        return false;
    }
    long long result = 0;
    for (const char c : value) {
        if (!std::isdigit(static_cast<unsigned char>(c))) {
            return false;
        }
        const int digit = c - '0';
        if (result > (std::numeric_limits<long long>::max() - digit) / 10) {
            return false;
        }
        result = result * 10 + digit;
    }
    out = result;
    return true;
}

}  // namespace

void strip_timing_markers(const std::string& text, TimingResult& out) {
    out = TimingResult{};
    out.stripped.reserve(text.size());
    std::size_t i = 0;
    while (i < text.size()) {
        const char c = text[i];
        if (c == '{' && i + 1 < text.size() && text[i + 1] == '{') {
            out.stripped.push_back('{');
            i += 2;
            continue;
        }
        if (c == '}' && i + 1 < text.size() && text[i + 1] == '}') {
            out.stripped.push_back('}');
            i += 2;
            continue;
        }
        if (c != '{') {
            out.stripped.push_back(c);
            ++i;
            continue;
        }

        const std::size_t close = text.find('}', i + 1);
        if (close == std::string::npos) {
            out.issues.push_back(
                TimingIssue{i, "unterminated marker ('{' without '}')"});
            out.stripped.append(text, i, std::string::npos);
            break;
        }
        const std::string body = text.substr(i + 1, close - i - 1);
        if (body.rfind("t=", 0) == 0) {
            long long time = 0;
            if (!parse_milliseconds(body.substr(2), time)) {
                out.issues.push_back(TimingIssue{
                    i, "invalid time in marker '{" + body + "}'"});
            } else {
                if (out.last_time >= 0 && time < out.last_time) {
                    out.issues.push_back(
                        TimingIssue{i, "timing goes backwards: " +
                                           std::to_string(time) +
                                           " ms after " +
                                           std::to_string(out.last_time) + " ms"});
                }
                out.last_time = time;
                ++out.marker_count;
            }
        } else {
            out.issues.push_back(
                TimingIssue{i, "unknown marker '{" + body + "}'"});
        }
        i = close + 1;
    }
}
