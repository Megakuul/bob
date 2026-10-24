#pragma once
#include <cstddef>
#include <vector>

namespace ring {

// Buffer is a fixed-capacity ring buffer of ints; push overwrites the oldest value.
class Buffer {
  public:
    explicit Buffer(std::size_t cap);
    void push(int v);
    std::size_t size() const { return size_; }
    int sum() const;

  private:
    std::vector<int> data_;
    std::size_t head_ = 0;
    std::size_t size_ = 0;
};

}  // namespace ring
