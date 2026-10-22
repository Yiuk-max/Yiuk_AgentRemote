#pragma once

#include <QByteArray>
#include <QObject>
#include <QString>

#include <functional>

class QProcess;

// Runs one command through pwsh.exe (falling back to powershell.exe) from a
// temporary .ps1 script that carries a UTF-8 BOM, with per-stream output capped
// at 1 MiB and a hard timeout. Both the platform path (results streamed back to
// the requesting Agent) and the local bridge path (results kept in the client)
// drive this class, so command execution has exactly one implementation.
class PowerShellRunner final : public QObject {
public:
    struct Hooks {
        // accepted: the bytes that fit inside the cap (empty once it is reached);
        // truncated: cumulative cap flag for that stream.
        std::function<void(const QByteArray &accepted, bool standardError, bool truncated)> onOutput;
        // status: succeeded | failed | timed_out | cancelled.
        // exitCode: -1 when the process never reported one (start error, timeout,
        // cancel). error: process error string for start/crash failures.
        std::function<void(const QString &status, int exitCode, const QString &error)> onFinished;
    };

    PowerShellRunner(QObject *parent, const QString &requestId, const QString &command,
                     const QString &workingDirectory, int timeoutSeconds, Hooks hooks);
    ~PowerShellRunner() override;

    // Returns false with *error set when the script or the interpreter cannot be
    // prepared; nothing is reported through onFinished in that case.
    bool start(QString *error);
    // Marks the command cancelled, stops it and reports the terminal state
    // through onFinished exactly once.
    void cancel();

private:
    void readOutput(bool standardError);
    void finish(const QString &status, int exitCode, const QString &error);
    void cleanup();

    QString requestId_;
    QString command_;
    QString workingDirectory_;
    int timeoutSeconds_;
    Hooks hooks_;
    QProcess *process_ = nullptr;
    QString scriptPath_;
    qint64 stdoutBytes_ = 0;
    qint64 stderrBytes_ = 0;
    bool stdoutTruncated_ = false;
    bool stderrTruncated_ = false;
    bool finished_ = false;
};
