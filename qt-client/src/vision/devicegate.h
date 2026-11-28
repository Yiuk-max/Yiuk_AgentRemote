#pragma once

#include <QString>

#include <atomic>
#include <condition_variable>
#include <mutex>

namespace yiuk::vision {

// DeviceGate 是**进程级**串行门：同一台设备上的重操作（抓屏 / 视觉）与输入动作
// 不能并发。会话级 SessionLimiter 只解决"同一个会话别连发"；两个会话同时
// 抓两块 2560x1600 全屏、同时拖两次鼠标时，缺的就是这一层。
//
// 这里只保证互斥、不设最小间隔：间隔是会话级语义，这里的进入/离开都很短。
// 排队期间通过 cancelled 标记响应取消，不能让取消操作等满一次全屏抓取。
class DeviceGate {
public:
    bool enter(const std::atomic_bool *cancelled, QString *error);
    void leave();

    // 诊断/测试用：当前是否被占用。
    bool busy() const;

private:
    mutable std::mutex mutex_;
    std::condition_variable cv_;
    bool busy_ = false;
};

}  // namespace yiuk::vision
