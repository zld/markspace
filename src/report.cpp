#include "color/report.hpp"

#include <cmath>
#include <iomanip>
#include <sstream>

#include "color/histogram.hpp"

namespace color {

namespace {

std::string fixed6(double value) {
    std::ostringstream out;
    out << std::fixed << std::setprecision(6) << value;
    return out.str();
}

std::string roundTrip6(double value) {
    // JSON numbers: use fixed notation unless it would lose magnitude.
    std::ostringstream out;
    if (value == 0.0) return "0.000000";
    if (std::fabs(value) >= 1e-4 && std::fabs(value) < 1e15) {
        out << std::fixed << std::setprecision(6) << value;
    } else {
        out << std::setprecision(17) << value;
    }
    return out.str();
}

std::string jsonHistogram(const Histogram& histogram) {
    std::ostringstream out;
    out << "[";
    for (std::size_t i = 0; i < histogram.buckets.size(); ++i) {
        if (i != 0) out << ",";
        out << histogram.buckets[i];
    }
    out << "]";
    return out.str();
}

std::string jsonSet(const ChannelHistograms& set) {
    std::ostringstream out;
    out << "{\"r\":" << jsonHistogram(set.r) << ",\"g\":" << jsonHistogram(set.g)
        << ",\"b\":" << jsonHistogram(set.b) << "}";
    return out.str();
}

std::string textHistogram(const std::string& name, const ChannelHistograms& set) {
    std::ostringstream out;
    out << "histogram_" << name << ":\n";
    const char* labels[3] = {"r", "g", "b"};
    const Histogram* histograms[3] = {&set.r, &set.g, &set.b};
    for (int i = 0; i < 3; ++i) {
        out << "  " << labels[i] << " [";
        for (std::size_t b = 0; b < histograms[i]->buckets.size(); ++b) {
            if (b != 0) out << ",";
            out << histograms[i]->buckets[b];
        }
        out << "] total=" << histograms[i]->total() << "\n";
    }
    return out.str();
}

}  // namespace

std::string validateCounts(const Report& report) {
    const std::uint64_t expected = static_cast<std::uint64_t>(report.pixels);
    const ChannelHistograms* sets[2] = {&report.before, &report.after};
    const char* names[2] = {"before", "after"};
    for (int s = 0; s < 2; ++s) {
        const Histogram* histograms[3] = {&sets[s]->r, &sets[s]->g, &sets[s]->b};
        const char* channels[3] = {"r", "g", "b"};
        for (int c = 0; c < 3; ++c) {
            if (histograms[c]->total() != expected) {
                std::ostringstream out;
                out << "histogram_" << names[s] << "." << channels[c] << " totals "
                    << histograms[c]->total() << ", expected " << expected;
                return out.str();
            }
        }
    }
    return {};
}

std::string reportDigest(const Report& report) {
    std::ostringstream out;
    out << "lut_size:" << report.lutSize << " pixels:" << report.pixels
        << " max_abs_diff:[" << fixed6(report.maxAbsDiff.r) << ","
        << fixed6(report.maxAbsDiff.g) << "," << fixed6(report.maxAbsDiff.b) << "]"
        << " threads:" << report.threads;
    return out.str();
}

namespace {
std::string jsonEscape(const std::string& text) {
    std::string out;
    out.reserve(text.size());
    for (char ch : text) {
        if (ch == '"' || ch == '\\') out.push_back('\\');
        out.push_back(ch);
    }
    return out;
}
}  // namespace

std::string renderJson(const Report& report) {
    std::ostringstream out;
    out << "{\n";
    out << "  \"input_space\":\"" << report.inputSpace << "\",\n";
    out << "  \"lut_space\":\"" << report.lutSpace << "\",\n";
    out << "  \"boundary\":\"" << report.boundary << "\",\n";
    out << "  \"lut_size\":" << report.lutSize << ",\n";
    out << "  \"lut_dims\":[" << report.lutDims[0] << "," << report.lutDims[1] << ","
        << report.lutDims[2] << "],\n";
    out << "  \"pixels\":" << report.pixels << ",\n";
    out << "  \"threads\":" << report.threads << ",\n";
    out << "  \"seed\":" << report.seed << ",\n";
    out << "  \"max_abs_diff\":[" << roundTrip6(report.maxAbsDiff.r) << ","
        << roundTrip6(report.maxAbsDiff.g) << "," << roundTrip6(report.maxAbsDiff.b)
        << "],\n";
    out << "  \"histogram_before\":" << jsonSet(report.before) << ",\n";
    out << "  \"histogram_after\":" << jsonSet(report.after) << ",\n";
    out << "  \"summary\":\"" << jsonEscape(reportDigest(report)) << "\"\n";
    out << "}\n";
    return out.str();
}

std::string renderText(const Report& report) {
    std::ostringstream out;
    out << "input_space:" << report.inputSpace << "\n";
    out << "lut_space:" << report.lutSpace << "\n";
    out << "boundary:" << report.boundary << "\n";
    out << "lut_size:" << report.lutSize << "\n";
    out << "lut_dims:" << report.lutDims[0] << "x" << report.lutDims[1] << "x"
        << report.lutDims[2] << "\n";
    out << "pixels:" << report.pixels << "\n";
    out << "threads:" << report.threads << "\n";
    out << "seed:" << report.seed << "\n";
    out << "max_abs_diff:[" << fixed6(report.maxAbsDiff.r) << "," << fixed6(report.maxAbsDiff.g)
        << "," << fixed6(report.maxAbsDiff.b) << "]\n";
    out << textHistogram("before", report.before);
    out << textHistogram("after", report.after);
    return out.str();
}

}  // namespace color
