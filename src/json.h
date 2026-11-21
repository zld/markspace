#pragma once

#include <map>
#include <string>
#include <vector>

// Small dependency-free JSON reader used for subtitle manifests. It accepts
// UTF-8 bytes verbatim inside strings (the subtitle decoder validates them
// later) and reports syntax errors with a 1-based line and column.
struct JsonValue {
    enum class Type { Null, Bool, Number, String, Array, Object };

    Type type = Type::Null;
    bool boolean = false;
    double number = 0.0;
    std::string string;
    std::vector<JsonValue> array;
    std::map<std::string, JsonValue> object;

    bool is_null() const { return type == Type::Null; }
    bool is_bool() const { return type == Type::Bool; }
    bool is_number() const { return type == Type::Number; }
    bool is_string() const { return type == Type::String; }
    bool is_array() const { return type == Type::Array; }
    bool is_object() const { return type == Type::Object; }

    const JsonValue* find(const std::string& key) const;
};

// Parses one complete JSON document. Returns false and fills `error` on a
// syntax failure, an over-deep document, or trailing non-whitespace.
bool parse_json(const std::string& text, JsonValue& out, std::string& error);
