// Deterministic fixture generator. Content is index-derived only.

#include "subtitle/fixture.hpp"

#include <cstdio>
#include <string>
#include <vector>

namespace subtitle {
namespace {

const char* kEnglish[] = {
    "The lighthouse keeper counts the waves.",
    "A quiet engine hums below the deck.",
    "She folded the map along the river.",
    "Dawn trade winds cross the harbor.",
};
const char* kChinese[] = {
    "夜色降临，港口的灯一盏盏亮起。",
    "他沿着旧铁路走了很远很远。",
    "字幕要在安全区内完整显示。",
    "风吹过麦田，像海浪一样起伏。",
};
const char* kMixed[] = {
    "版本 v2.1 修复了渲染管线的问题。",
    "帧率稳定在 60fps，延迟低于 16ms。",
    "玩家 HP 归零时触发结算动画。",
};
const char* kCombining[] = {
    "Cafe\xCC\x81" " au lait, nai\xCC\x88" "ve facade.",
    "a\xCC\x81" "e\xCC\x81" "i\xCC\x81" "o\xCC\x81" "u\xCC\x81" " 组合符号测试。",
};
const char* kPunct[] = {
    "「引号」、（括号）。《书名号》测试！",
    "你好，世界；再见，昨天？是的……",
};
const char* kExplicit[] = {
    "第一行在这里\nSecond line follows here",
    "上句很短\nAnd this one wraps nicely too",
};
const char* kUrl[] = {
    "详见 https://example.com/docs?v=12 说明。",
    "镜像站 www.mirror.example.net 已同步。",
};

std::string long_token(std::size_t i) {
  // 64-char unbreakable alphanumeric token (~1200px at 32px: overflows).
  std::string s = "ID";
  for (std::size_t k = 0; k < 64; ++k)
    s.push_back(static_cast<char>('A' + (i * 7 + k * 13) % 26));
  return s;
}

std::string timestamp(std::int64_t ms) {
  char buf[32];
  std::snprintf(buf, sizeof buf, "%02lld:%02lld:%02lld,%03lld",
                static_cast<long long>(ms / 3600000),
                static_cast<long long>(ms / 60000 % 60),
                static_cast<long long>(ms / 1000 % 60),
                static_cast<long long>(ms % 1000));
  return buf;
}

}  // namespace

std::string generate_fixture(std::size_t count) {
  std::string out;
  for (std::size_t i = 0; i < count; ++i) {
    std::string text;
    if (i % 16 == 15) {
      text = long_token(i);
    } else {
      switch (i % 7) {
        case 0: text = kEnglish[(i / 7) % 4]; break;
        case 1: text = kChinese[(i / 7) % 4]; break;
        case 2: text = kMixed[(i / 7) % 3]; break;
        case 3: text = kCombining[(i / 7) % 2]; break;
        case 4: text = kPunct[(i / 7) % 2]; break;
        case 5: text = kExplicit[(i / 7) % 2]; break;
        default: text = kUrl[(i / 7) % 2]; break;
      }
    }
    const std::int64_t start = static_cast<std::int64_t>(i) * 2000;
    out += std::to_string(i + 1);
    out += '\n';
    out += timestamp(start) + " --> " + timestamp(start + 1500);
    out += '\n';
    out += text;
    out += "\n\n";
  }
  return out;
}

}  // namespace subtitle
