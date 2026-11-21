#include "json.h"

#include <cerrno>
#include <cmath>
#include <cstdlib>

namespace {

constexpr int kMaxDepth = 128;

class Parser {
public:
    explicit Parser(const std::string& text) : text_(text) {}

    bool parse(JsonValue& out, std::string& error) {
        skip_whitespace();
        if (!parse_value(out, 0)) {
            error = error_;
            return false;
        }
        skip_whitespace();
        if (pos_ != text_.size()) {
            fail("unexpected trailing characters");
        }
        error = error_;
        return error_.empty();
    }

private:
    const std::string& text_;
    std::size_t pos_ = 0;
    std::size_t line_ = 1;
    std::size_t column_ = 1;
    std::string error_;

    bool at_end() const { return pos_ >= text_.size(); }
    char peek() const { return at_end() ? '\0' : text_[pos_]; }
    static int hex_digit(char c) {
        if (c >= '0' && c <= '9') return c - '0';
        if (c >= 'a' && c <= 'f') return c - 'a' + 10;
        if (c >= 'A' && c <= 'F') return c - 'A' + 10;
        return -1;
    }

    void advance() {
        if (at_end()) return;
        if (text_[pos_++] == '\n') { ++line_; column_ = 1; } else { ++column_; }
    }

    bool fail(const std::string& message) {
        if (error_.empty()) {
            error_ = "line " + std::to_string(line_) + ", column " +
                     std::to_string(column_) + ": " + message;
        }
        return false;
    }

    void skip_whitespace() {
        while (!at_end() && (peek() == ' ' || peek() == '\t' || peek() == '\r' ||
                             peek() == '\n')) {
            advance();
        }
    }

    bool literal(const char* word) {
        for (const char* p = word; *p != '\0'; ++p) {
            if (at_end() || peek() != *p) {
                return fail(std::string("expected '") + word + "'");
            }
            advance();
        }
        return true;
    }

    bool parse_value(JsonValue& out, int depth) {
        if (depth > kMaxDepth) return fail("document nesting is too deep");
        if (at_end()) return fail("expected a value");
        switch (peek()) {
            case '{': return parse_object(out, depth);
            case '[': return parse_array(out, depth);
            case '"':
                out.type = JsonValue::Type::String;
                return parse_string(out.string);
            case 't':
                out.type = JsonValue::Type::Bool;
                out.boolean = true;
                return literal("true");
            case 'f':
                out.type = JsonValue::Type::Bool;
                out.boolean = false;
                return literal("false");
            case 'n':
                out.type = JsonValue::Type::Null;
                return literal("null");
            default:
                if (peek() == '-' || (peek() >= '0' && peek() <= '9')) {
                    return parse_number(out);
                }
                return fail("expected a value");
        }
    }

    bool parse_object(JsonValue& out, int depth) {
        out.type = JsonValue::Type::Object;
        advance();
        skip_whitespace();
        if (peek() == '}') {
            advance();
            return true;
        }
        while (true) {
            skip_whitespace();
            if (peek() != '"') return fail("expected an object key");
            std::string key;
            if (!parse_string(key)) return false;
            skip_whitespace();
            if (peek() != ':') return fail("expected ':' after object key");
            advance();
            skip_whitespace();
            JsonValue value;
            if (!parse_value(value, depth + 1)) return false;
            out.object[std::move(key)] = std::move(value);
            skip_whitespace();
            if (peek() == ',') { advance(); continue; }
            if (peek() == '}') { advance(); return true; }
            return fail("expected ',' or '}' in object");
        }
    }

    bool parse_array(JsonValue& out, int depth) {
        out.type = JsonValue::Type::Array;
        advance();
        skip_whitespace();
        if (peek() == ']') {
            advance();
            return true;
        }
        while (true) {
            skip_whitespace();
            JsonValue value;
            if (!parse_value(value, depth + 1)) return false;
            out.array.push_back(std::move(value));
            skip_whitespace();
            if (peek() == ',') { advance(); continue; }
            if (peek() == ']') { advance(); return true; }
            return fail("expected ',' or ']' in array");
        }
    }

    bool parse_hex4(unsigned& value) {
        value = 0;
        for (int i = 0; i < 4; ++i) {
            if (at_end()) return fail("truncated \\u escape");
            const int digit = hex_digit(peek());
            if (digit < 0) return fail("invalid hex digit in \\u escape");
            value = (value << 4) | static_cast<unsigned>(digit);
            advance();
        }
        return true;
    }

    static void append_utf8(std::string& out, unsigned cp) {
        if (cp < 0x80) {
            out.push_back(static_cast<char>(cp));
        } else if (cp < 0x800) {
            out.push_back(static_cast<char>(0xC0 | (cp >> 6)));
            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
        } else if (cp < 0x10000) {
            out.push_back(static_cast<char>(0xE0 | (cp >> 12)));
            out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
        } else {
            out.push_back(static_cast<char>(0xF0 | (cp >> 18)));
            out.push_back(static_cast<char>(0x80 | ((cp >> 12) & 0x3F)));
            out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
            out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
        }
    }

    bool parse_string(std::string& out) {
        advance();
        out.clear();
        while (true) {
            if (at_end()) return fail("unterminated string");
            const unsigned char c = static_cast<unsigned char>(peek());
            if (c == '"') {
                advance();
                return true;
            }
            if (c < 0x20) return fail("control character in string");
            if (c != '\\') {
                out.push_back(static_cast<char>(c));
                advance();
                continue;
            }
            advance();
            if (at_end()) return fail("unterminated escape sequence");
            const char esc = peek();
            advance();
            const char* simple = nullptr;
            switch (esc) {
                case '"': simple = "\""; break;
                case '\\': simple = "\\"; break;
                case '/': simple = "/"; break;
                case 'b': simple = "\b"; break;
                case 'f': simple = "\f"; break;
                case 'n': simple = "\n"; break;
                case 'r': simple = "\r"; break;
                case 't': simple = "\t"; break;
                case 'u': {
                    unsigned cp = 0;
                    if (!parse_hex4(cp)) return false;
                    if (cp >= 0xD800 && cp <= 0xDBFF) {
                        unsigned low = 0;
                        if (at_end() || peek() != '\\') {
                            return fail("high surrogate without low surrogate");
                        }
                        advance();
                        if (at_end() || peek() != 'u') {
                            return fail("high surrogate without low surrogate");
                        }
                        advance();
                        if (!parse_hex4(low)) return false;
                        if (low < 0xDC00 || low > 0xDFFF) {
                            return fail("invalid low surrogate");
                        }
                        cp = 0x10000 + ((cp - 0xD800) << 10) + (low - 0xDC00);
                    } else if (cp >= 0xDC00 && cp <= 0xDFFF) {
                        return fail("lone low surrogate");
                    }
                    append_utf8(out, cp);
                    continue;
                }
                default: return fail("invalid escape sequence");
            }
            out.push_back(*simple);
        }
    }

    bool parse_number(JsonValue& out) {
        const std::size_t begin = pos_;
        if (peek() == '-') advance();
        if (at_end() || peek() < '0' || peek() > '9') {
            return fail("invalid number");
        }
        if (peek() == '0') {
            advance();
        } else {
            while (!at_end() && peek() >= '0' && peek() <= '9') advance();
        }
        if (!at_end() && peek() == '.') {
            advance();
            if (at_end() || peek() < '0' || peek() > '9') {
                return fail("invalid number fraction");
            }
            while (!at_end() && peek() >= '0' && peek() <= '9') advance();
        }
        if (!at_end() && (peek() == 'e' || peek() == 'E')) {
            advance();
            if (!at_end() && (peek() == '+' || peek() == '-')) advance();
            if (at_end() || peek() < '0' || peek() > '9') {
                return fail("invalid number exponent");
            }
            while (!at_end() && peek() >= '0' && peek() <= '9') advance();
        }
        const std::string token = text_.substr(begin, pos_ - begin);
        errno = 0;
        char* end = nullptr;
        const double value = std::strtod(token.c_str(), &end);
        if (errno == ERANGE || end == nullptr || *end != '\0' ||
            !std::isfinite(value)) {
            return fail("number out of range");
        }
        out.type = JsonValue::Type::Number;
        out.number = value;
        return true;
    }
};

}  // namespace

const JsonValue* JsonValue::find(const std::string& key) const {
    if (type != Type::Object) return nullptr;
    const auto it = object.find(key);
    return it == object.end() ? nullptr : &it->second;
}

bool parse_json(const std::string& text, JsonValue& out, std::string& error) {
    out = JsonValue{};
    Parser parser(text);
    return parser.parse(out, error);
}
