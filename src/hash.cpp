#include "hash.hpp"

namespace tl {

uint64_t fnv1a64(const void* data, size_t len) {
  const auto* p = static_cast<const uint8_t*>(data);
  uint64_t h = 14695981039346656037ull;  // offset basis
  for (size_t i = 0; i < len; ++i) {
    h ^= p[i];
    h *= 1099511628211ull;  // FNV prime
  }
  return h;
}

}  // namespace tl
