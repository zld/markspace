// subtitle-audit: ASS document model, field parsing and sample generation.
#pragma once

#include <cstddef>
#include <map>
#include <string>
#include <vector>

namespace subaudit {

// Hard input limits so audit runs stay bounded and reproducible.
constexpr std::size_t kMaxInputBytes = 32u * 1024u * 1024u;  // 32 MiB
constexpr std::size_t kMaxEvents = 10000;

// One Style: line under [V4+ Styles], keyed by the section's Format:
// column names. Values are raw strings; the optional Parent extension
// and "~" inherit markers are resolved later by style_graph.
struct Style {
    std::string name;
    std::map<std::string, std::string> fields;
    int line = 0;  // 1-based source line
};

// One Dialogue:/Comment: line under [Events].
struct Event {
    int id = 0;          // 1-based order in the file
    int line = 0;        // 1-based source line
    std::string kind;    // "Dialogue" or "Comment"
    std::string style;   // value of the Style column
    std::string text;    // raw Text column, commas preserved
    long long start_ms = 0;
    long long end_ms = 0;
    std::map<std::string, std::string> fields;
};

struct Document {
    int play_res_x = 384;
    int play_res_y = 288;
    std::vector<std::string> style_format;
    std::vector<std::string> event_format;
    std::vector<Style> styles;
    std::vector<Event> events;
};

// Parses UTF-8 ASS/SSA v4+ text. Handles the BOM, CRLF line endings and
// commas inside the Text column; reports line-numbered errors for missing
// columns, malformed times and negative durations. Returns false and fills
// err on the first problem.
bool parse_ass(const std::string& data, Document& doc, std::string& err);

// Strict "h:mm:ss.cc" parse. Rejects negatives and out-of-range fields.
bool parse_ass_time(const std::string& s, long long& ms);

// Builds the demo fixture: a 3-level style inheritance chain and six
// dialogue events exercising recognized, unknown and malformed tags.
std::string generate_demo_ass();

}  // namespace subaudit
