#pragma once

#include <map>
#include <string>
#include <vector>

namespace sp {

enum class Severity { Warning, Error };

struct Diagnostic {
    Severity severity = Severity::Warning;
    int line = 0;
    std::string code;
    std::string message;
    int event_id = -1;
};

const char* severity_name(Severity severity);
std::string sanitize_for_output(const std::string& text, size_t max_len = 240);
std::string trim_ascii(const std::string& text);
std::string lower_ascii(std::string text);
bool parse_ass_time(const std::string& text, long long& out_ms);
std::vector<std::string> split_commas(const std::string& text, size_t max_fields);

// One ASS style column. `present` is false when the column is absent from the
// Format line or the style leaves it blank (inherit from parent). An explicit
// empty is written as "" in the fixture format and overrides a parent value.
struct FieldValue {
    bool present = false;
    bool explicit_empty = false;
    std::string value;
};

struct RawStyle {
    std::string name;
    std::string parent;
    int line = 0;
    std::map<std::string, FieldValue> fields;
};

struct ScriptInfo {
    int play_res_x = 0;
    int play_res_y = 0;
    int wrap_style = 0;
    bool scaled_border_and_shadow = true;
    std::string ycbcr_matrix;
};

struct Document {
    ScriptInfo info;
    std::vector<RawStyle> styles;  // unique names, first definition wins
    std::vector<Diagnostic> diagnostics;
    int style_line_count = 0;
    std::string source_name;

    bool has_error() const;
};

Document parse_ass_document(const std::string& content, const std::string& source_name);

}  // namespace sp
