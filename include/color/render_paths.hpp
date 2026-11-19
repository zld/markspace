#pragma once

#include <cstddef>
#include <vector>

#include "color/types.hpp"

namespace color {

struct Chunk {
    std::size_t begin = 0;
    std::size_t end = 0;
};

// Fixed, order-preserving partition: the first `total % threads` chunks carry
// one extra pixel. Same input always yields the same chunk boundaries.
std::vector<Chunk> partition(std::size_t total, int threads);

// Reference path: single-threaded, sequentially calls the shared kernel.
std::vector<RGB> renderScalar(const LUT& lut, const std::vector<RGB>& pixels,
                              BoundaryPolicy policy);

// Parallel path: fixed chunks, one thread per chunk, each thread writes only
// its own slice so pixel order is identical to the scalar path. If any worker
// throws, the first failure (in chunk order) is rethrown after all threads are
// joined and no partial result is returned.
std::vector<RGB> renderParallel(const LUT& lut, const std::vector<RGB>& pixels,
                                BoundaryPolicy policy, int threads);

// Per-channel maximum absolute difference. Throws ProcessingError when the
// two buffers differ in size.
RGB maxAbsDiff(const std::vector<RGB>& lhs, const std::vector<RGB>& rhs);

}  // namespace color
