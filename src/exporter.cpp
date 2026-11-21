#include "exporter.h"

#include <unistd.h>

#include <cerrno>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <sstream>

namespace {

std::string escape_into(const std::string& value) {
    std::string out;
    out.reserve(value.size() + 8);
    for (const unsigned char c : value) {
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\b': out += "\\b"; break;
            case '\f': out += "\\f"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (c < 0x20) {
                    char buffer[7];
                    std::snprintf(buffer, sizeof(buffer), "\\u%04x", c);
                    out += buffer;
                } else {
                    out.push_back(static_cast<char>(c));
                }
        }
    }
    return out;
}

std::string join(const std::vector<std::string>& values) {
    std::string out = "[";
    for (std::size_t i = 0; i < values.size(); ++i) {
        if (i != 0) out += ",";
        out += "\"" + escape_into(values[i]) + "\"";
    }
    out += "]";
    return out;
}

}  // namespace

std::string json_escape(const std::string& value) {
    return escape_into(value);
}

std::string summary_to_json(const SubtitleSummary& summary) {
    std::ostringstream out;
    out << "{\"id\":\"" << escape_into(summary.id) << "\",\"language\":\""
        << escape_into(summary.language) << "\",\"lines\":" << summary.line_count
        << ",\"bbox\":{\"x\":" << summary.bbox.x << ",\"y\":" << summary.bbox.y
        << ",\"width\":" << summary.bbox.width
        << ",\"height\":" << summary.bbox.height << "},\"line_breaks\":[";
    for (std::size_t i = 0; i < summary.line_breaks.size(); ++i) {
        if (i != 0) out << ",";
        out << summary.line_breaks[i];
    }
    out << "],\"line_ranges\":[";
    for (std::size_t i = 0; i < summary.line_ranges.size(); ++i) {
        if (i != 0) out << ",";
        out << "{\"begin\":" << summary.line_ranges[i].begin_cluster
            << ",\"end\":" << summary.line_ranges[i].end_cluster << "}";
    }
    out << "],\"break_reasons\":" << join(summary.break_reasons)
        << ",\"overflow_count\":" << summary.overflow_count
        << ",\"overflow_reasons\":" << join(summary.overflow_reasons)
        << ",\"missing_glyphs\":" << summary.missing_glyphs
        << ",\"errors\":" << join(summary.errors) << "}";
    return out.str();
}

bool write_summary_atomic(const std::string& path,
                          const std::vector<SubtitleSummary>& summaries,
                          std::string& error) {
    const std::string temporary =
        path + ".tmp." + std::to_string(static_cast<long long>(getpid()));
    {
        std::ofstream out(temporary, std::ios::binary | std::ios::trunc);
        if (!out) {
            error = "cannot create temporary summary: " + temporary;
            return false;
        }
        for (const SubtitleSummary& summary : summaries) {
            out << summary_to_json(summary) << '\n';
        }
        out.flush();
        if (!out) {
            error = "failed while writing temporary summary: " + temporary;
            out.close();
            std::remove(temporary.c_str());
            return false;
        }
    }
    if (std::rename(temporary.c_str(), path.c_str()) != 0) {
        error = "cannot replace " + path + ": " + std::strerror(errno);
        std::remove(temporary.c_str());
        return false;
    }
    return true;
}
