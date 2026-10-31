#include "powershellrunner.h"

#include <QDir>
#include <QFile>
#include <QProcess>
#include <QStandardPaths>
#include <QTimer>

namespace {

constexpr qint64 kOutputLimitPerStream = 1 << 20;
constexpr int kMinTimeoutSeconds = 1;
constexpr int kMaxTimeoutSeconds = 3600;

// Windows PowerShell 5.1 会将未以 BOM 开头的 .ps1 文件按 ANSI 解码，因此非 ASCII 命令会乱码
// 预置脚本会强制使用 UTF-8 输出，确保两种解释器返回相同的字节
bool writeScriptFile(const QString &path, const QString &command, QString *error) {
    QFile script(path);
    if (!script.open(QIODevice::WriteOnly)) {
        if (error) *error = QStringLiteral("无法创建临时 PowerShell 脚本");
        return false;
    }
    script.write(QByteArray::fromHex("efbbbf"));
    script.write("[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false\r\n"
                 "$OutputEncoding = [Console]::OutputEncoding\r\n");
    script.write(command.toUtf8());
    script.close();
    return true;
}

QString locatePowerShell() {
    QString program = QStandardPaths::findExecutable(QStringLiteral("pwsh.exe"));
    if (program.isEmpty()) program = QStandardPaths::findExecutable(QStringLiteral("powershell.exe"));
    return program;
}

}  // namespace


PowerShellRunner::PowerShellRunner(QObject *parent, const QString &requestId, const QString &command,
                                   const QString &workingDirectory, int timeoutSeconds, Hooks hooks)
    : QObject(parent),
      requestId_(requestId),
      command_(command),
      workingDirectory_(workingDirectory),
      timeoutSeconds_(qBound(kMinTimeoutSeconds, timeoutSeconds, kMaxTimeoutSeconds)),
      hooks_(std::move(hooks)) {}

PowerShellRunner::~PowerShellRunner() {
    cleanup();
}

bool PowerShellRunner::start(QString *error) {
    scriptPath_ = QDir(QStandardPaths::writableLocation(QStandardPaths::TempLocation))
                      .filePath(QStringLiteral("yiuk-") + requestId_ + QStringLiteral(".ps1"));
    if (!writeScriptFile(scriptPath_, command_, error)) return false;

    const QString program = locatePowerShell();
    if (program.isEmpty()) {
        if (error) *error = QStringLiteral("未找到 PowerShell 可执行程序");
        cleanup();
        return false;
    }

    process_ = new QProcess(this);
    process_->setProcessChannelMode(QProcess::SeparateChannels);
    process_->setProgram(program);
    process_->setArguments({"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
                            "-File", scriptPath_});
    if (QDir(workingDirectory_).exists()) process_->setWorkingDirectory(workingDirectory_);

    connect(process_, &QProcess::readyReadStandardOutput, this, [this] { readOutput(false); });
    connect(process_, &QProcess::readyReadStandardError, this, [this] { readOutput(true); });
    connect(process_, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), this,
            [this](int exitCode, QProcess::ExitStatus exitStatus) {
                readOutput(false);
                readOutput(true);
                if (finished_) return;
                const bool success = exitStatus == QProcess::NormalExit && exitCode == 0;
                finish(success ? QStringLiteral("succeeded") : QStringLiteral("failed"), exitCode, QString());
            });
    connect(process_, &QProcess::errorOccurred, this, [this](QProcess::ProcessError) {
        if (finished_ || !process_ || process_->state() != QProcess::NotRunning) return;
        finish(QStringLiteral("failed"), -1, process_->errorString());
    });
    QTimer::singleShot(timeoutSeconds_ * 1000, this, [this] {
        if (finished_) return;
        finish(QStringLiteral("timed_out"), -1, QString());
    });

    process_->start();
    return true;
}

void PowerShellRunner::cancel() {
    if (finished_) return;
    finish(QStringLiteral("cancelled"), -1, QString());
}

void PowerShellRunner::readOutput(bool standardError) {
    if (!process_) return;
    QByteArray bytes = standardError ? process_->readAllStandardError() : process_->readAllStandardOutput();
    if (bytes.isEmpty()) return;

    qint64 &budget = standardError ? stderrBytes_ : stdoutBytes_;
    bool &truncated = standardError ? stderrTruncated_ : stdoutTruncated_;
    const qint64 available = qMax<qint64>(0, kOutputLimitPerStream - budget);
    if (bytes.size() > available) {
        bytes.truncate(static_cast<int>(available));
        truncated = true;
    }
    budget += bytes.size();
    if (hooks_.onOutput) hooks_.onOutput(bytes, standardError, truncated);
}

void PowerShellRunner::finish(const QString &status, int exitCode, const QString &error) {
    if (finished_) return;
    finished_ = true;
    // cleanup() 会先断开进程信号，因此在此终止进程不会回调 finish() 并重复报告最终状态
    cleanup();
    if (hooks_.onFinished) hooks_.onFinished(status, exitCode, error);
}

void PowerShellRunner::cleanup() {
    if (process_) {
        QProcess *process = process_;
        process_ = nullptr;
        process->disconnect(this);
        if (process->state() != QProcess::NotRunning) {
            process->kill();
            process->waitForFinished(1000);
        }
        process->deleteLater();
    }
    if (!scriptPath_.isEmpty()) {
        QFile::remove(scriptPath_);
        scriptPath_.clear();
    }
}
