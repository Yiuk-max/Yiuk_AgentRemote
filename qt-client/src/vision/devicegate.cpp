#include "devicegate.h"

#include <chrono>

namespace yiuk::vision {

bool DeviceGate::enter(const std::atomic_bool *cancelled, QString *error) {
    std::unique_lock<std::mutex> lock(mutex_);
    while (busy_) {
        if (cancelled != nullptr && cancelled->load()) {
            if (error) *error = QStringLiteral("设备正忙，且本次操作已被取消");
            return false;
        }
        cv_.wait_for(lock, std::chrono::milliseconds(10));
    }
    busy_ = true;
    return true;
}

void DeviceGate::leave() {
    std::lock_guard<std::mutex> lock(mutex_);
    busy_ = false;
    cv_.notify_all();
}

bool DeviceGate::busy() const {
    std::lock_guard<std::mutex> lock(mutex_);
    return busy_;
}

}  // namespace yiuk::vision
