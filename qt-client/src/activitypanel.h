#pragma once

#include <QObject>
#include <QString>
#include <QVector>

class QProcess;

// 面板渲染的条目类型；类型决定条目的文字颜色
// 与 src/panel/paneltypes.h 中的 yiuk::Kind 对应；activitypanel.cpp 中的协议标记用于保持两个进程间一致
enum class ActivityKind {
    Info,
    Session,
    Execute,
    Output,
    Error,
    Read,
    Download,
    Upload,
    Success,
};

struct ActivityEntry {
    ActivityKind kind = ActivityKind::Info;
    QString time;  // 时间格式为 "HH:MM:SS"
    QString text;  // 可能包含 '\n'
};

// 当前会话的只读活动日志运行在独立进程 yiuk-activity-panel.exe 中
// 这是原生逐像素透明的 Win32 窗口，所有者可随时关闭而不影响会话
// 此类只负责管理该子进程，并向其 stdin 写入协议行
class ActivityPanel final : public QObject {
public:
    explicit ActivityPanel(QObject *parent = nullptr);
    ~ActivityPanel() override;

    bool isOpen() const;

    // 面板未运行时启动它并重放 `history`；配套可执行文件不存在时返回 false
    bool open(const QString &title, const QString &label, const QString &state, const QString &status,
              const QVector<ActivityEntry> &history);

    // 替换面板内容（新的当前会话）和状态
    void reset(const QString &label, const QString &state, const QString &status,
               const QVector<ActivityEntry> &history);

    void append(const ActivityEntry &entry);
    void setStatus(const QString &state, const QString &status);

    // 仅在用户主动请求窗口时调用；不能因新活动而将窗口重新置于前台
    void activate();

    void close();

    void setSessionId(const QString &id) { currentSession = id; }
    QString sessionId() const { return currentSession; }

private:
    void send(const QString &line);
    void cleanup();

    QProcess *process = nullptr;
    QString currentSession;
    QString lastState;
    QString lastStatus;
};
