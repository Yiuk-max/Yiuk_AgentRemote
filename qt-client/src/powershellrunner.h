#pragma once

#include <QByteArray>
#include <QObject>
#include <QString>

#include <functional>

class QProcess;

// 通过 pwsh.exe（不可用时回退到 powershell.exe）执行一条命令
// 命令写入带 UTF-8 BOM 的临时 .ps1 脚本；每个输出流最多保留 1 MiB，并设置硬超时
// 平台路径（结果流式返回给请求的 Agent）和本地桥接路径（结果保留在客户端）都使用此类
// 因此命令执行逻辑只有一份实现
class PowerShellRunner final : public QObject {
public:
    struct Callbacks {
        // retainedOutput：输出额度内保留的字节，达到上限后为空
        // truncated：该输出流是否曾达到限制的累计标记
        std::function<void(const QByteArray &retainedOutput, bool isStandardError, bool truncated)> onOutput;
        // status：succeeded | failed | timed_out | cancelled
        // exitCode：进程未报告退出码时为 -1（启动错误、超时或取消）
        // error：启动或崩溃失败时的进程错误信息
        std::function<void(const QString &status, int exitCode, const QString &error)> onFinished;
    };

    PowerShellRunner(QObject *parent, const QString &requestId, const QString &command,
                     const QString &workingDirectory, int timeoutSeconds, Callbacks callbacks);
    ~PowerShellRunner() override;

    // 无法准备脚本或解释器时返回 false 并设置 *error；此时不会触发 onFinished
    bool start(QString *error);
    // 将命令标记为已取消并停止执行，通过 onFinished 恰好报告一次最终状态
    void cancel();

private:
    void readAndLimitOutput(bool isStandardError);
    void finishAndReport(const QString &status, int exitCode, const QString &error);
    void stopProcessAndRemoveScript();

    QString requestId_;
    QString command_;
    QString workingDirectory_;
    int timeoutSeconds_;
    Callbacks callbacks_;
    QProcess *process_ = nullptr;
    QString scriptPath_;
    qint64 stdoutRetainedBytes_ = 0;
    qint64 stderrRetainedBytes_ = 0;
    bool stdoutTruncated_ = false;
    bool stderrTruncated_ = false;
    bool finished_ = false;
};
