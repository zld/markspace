#include "color/render_paths.hpp"

#include <algorithm>
#include <cmath>
#include <exception>
#include <string>
#include <thread>

#include "color/lut_kernel.hpp"

namespace color {

namespace {

void validateRequest(const LUT& lut, const std::vector<RGB>& pixels, int threads) {
    validateLut(lut);
    if (pixels.size() > kMaxPixels) {
        throw ProcessingError("pixel count exceeds limit: " + std::to_string(pixels.size()));
    }
    if (threads < 1 || threads > kMaxThreads) {
        throw std::invalid_argument("thread count must be in [1," +
                                    std::to_string(kMaxThreads) + "]");
    }
}

}  // namespace

std::vector<Chunk> partition(std::size_t total, int threads) {
    if (threads < 1 || threads > kMaxThreads) {
        throw std::invalid_argument("thread count must be in [1," +
                                    std::to_string(kMaxThreads) + "]");
    }
    std::vector<Chunk> chunks;
    const std::size_t count = std::min<std::size_t>(static_cast<std::size_t>(threads), total);
    const std::size_t base = total / static_cast<std::size_t>(threads);
    const std::size_t remainder = total % static_cast<std::size_t>(threads);
    std::size_t cursor = 0;
    for (std::size_t i = 0; i < count; ++i) {
        const std::size_t length = base + (i < remainder ? 1 : 0);
        chunks.push_back({cursor, cursor + length});
        cursor += length;
    }
    return chunks;
}

std::vector<RGB> renderScalar(const LUT& lut, const std::vector<RGB>& pixels,
                              BoundaryPolicy policy) {
    validateRequest(lut, pixels, 1);
    std::vector<RGB> output;
    output.reserve(pixels.size());
    for (std::size_t i = 0; i < pixels.size(); ++i) {
        output.push_back(applyLut(lut, pixels[i], policy, i));
    }
    return output;
}

std::vector<RGB> renderParallel(const LUT& lut, const std::vector<RGB>& pixels,
                                BoundaryPolicy policy, int threads) {
    validateRequest(lut, pixels, threads);
    const std::vector<Chunk> chunks = partition(pixels.size(), threads);
    std::vector<RGB> output(pixels.size());
    std::vector<std::exception_ptr> failures(chunks.size());

    auto run = [&](std::size_t chunkIndex) {
        const Chunk& chunk = chunks[chunkIndex];
        for (std::size_t i = chunk.begin; i < chunk.end; ++i) {
            output[i] = applyLut(lut, pixels[i], policy, i);
        }
    };

    std::vector<std::thread> workers;
    workers.reserve(chunks.size());
    for (std::size_t i = 0; i < chunks.size(); ++i) {
        workers.emplace_back([&, i] {
            try {
                run(i);
            } catch (...) {
                failures[i] = std::current_exception();
            }
        });
    }
    for (std::thread& worker : workers) worker.join();

    // Rethrow in chunk order so repeated runs report the same first failure.
    for (std::size_t i = 0; i < failures.size(); ++i) {
        if (!failures[i]) continue;
        try {
            std::rethrow_exception(failures[i]);
        } catch (const ColorError&) {
            throw;
        } catch (const ProcessingError&) {
            throw;
        } catch (const std::exception& error) {
            throw ProcessingError("worker thread failure: " + std::string(error.what()));
        } catch (...) {
            throw ProcessingError("worker thread failure: unknown exception");
        }
    }
    return output;
}

RGB maxAbsDiff(const std::vector<RGB>& lhs, const std::vector<RGB>& rhs) {
    if (lhs.size() != rhs.size()) {
        throw ProcessingError("cannot compare buffers of different size: " +
                              std::to_string(lhs.size()) + " vs " + std::to_string(rhs.size()));
    }
    RGB worst{0.0, 0.0, 0.0};
    for (std::size_t i = 0; i < lhs.size(); ++i) {
        for (int channel = 0; channel < 3; ++channel) {
            const double a = lhs[i][channel];
            const double b = rhs[i][channel];
            if (!std::isfinite(a) || !std::isfinite(b)) {
                throw ProcessingError("non-finite result at pixel " + std::to_string(i));
            }
            double& slot = worst[channel];
            slot = std::max(slot, std::fabs(a - b));
        }
    }
    return worst;
}

}  // namespace color
