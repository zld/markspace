// subtitle-audit: inline override tag scanning.
#pragma once

#include <map>
#include <string>

namespace subaudit {

struct TagStats {
    std::map<std::string, int> recognized_counts;
    std::map<std::string, int> unknown_counts;
    int recognized_total = 0;
    int unknown_total = 0;
    int malformed = 0;  // unclosed '{' or unclosed \t(...) group

    void add_recognized(const std::string& name) {
        ++recognized_counts[name];
        ++recognized_total;
    }
    void add_unknown(const std::string& name) {
        ++unknown_counts[name];
        ++unknown_total;
    }
};

// Scans one event Text: override blocks {...}, nested \t(...) transforms,
// style resets \r and the \N \n \h escapes. Unknown tags are counted and
// kept; unclosed braces/groups bump the malformed counter and stop the
// scan of that text.
void scan_event_text(const std::string& text, TagStats& stats);

}  // namespace subaudit
