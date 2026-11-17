#pragma once

#include <cstddef>
#include <cstdint>

namespace tl {

// FNV-1a 64-bit hash over a byte range.
uint64_t fnv1a64(const void* data, size_t len);

}  // namespace tl
