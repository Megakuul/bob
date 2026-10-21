#include "ring/ring.hpp"

#include <numeric>

namespace ring {

Buffer::Buffer(std::size_t cap) : data_(cap) {}

void Buffer::push(int v) {
    data_[head_] = v;
    head_ = (head_ + 1) % data_.size();
    if (size_ < data_.size()) size_++;
}

int Buffer::sum() const { return std::accumulate(data_.begin(), data_.begin() + size_, 0); }

}  // namespace ring
