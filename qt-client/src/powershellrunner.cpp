#include "powershellrunner.h"

#include "vision/apppaths.h"

#include <QDir>
#include <QFile>
#include <QProcess>

#ifdef Q_OS_WIN
#include <windows.h>
#endif
#include <QStandardPaths>
#include <QTimer>

namespace {

constexpr qint64 kOutputLimitBytesPerStream = 1 << 20;
constexpr int kMinTimeoutSeconds = 1;
constexpr int kMaxTimeoutSeconds = 3600;

// Windows PowerShell 5.1 会将未以 BOM 开头的 .ps1 文件按 ANSI 解码，因此非 ASCII 命令会乱码
// 预置脚本会强制使用 UTF-8 输出，确保两种解释器返回相同的字节
bool writeUtf8PowerShellScript(const QString &path, const QString &command, QString *error) {
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

QString findPowerShellExecutable() {
    QString executablePath = QStandardPaths::findExecutable(QStringLiteral("pwsh.exe"));
    if (executablePath.isEmpty()) executablePath = QStandardPaths::findExecutable(QStringLiteral("powershell.exe"));
    return executablePath;
}

}  // namespace


PowerShellRunner::PowerShellRunner(QObject *parent, const QString &requestId, const QString &command,
                                   const QString &workingDirectory, int timeoutSeconds, Callbacks callbacks)
    : QObject(parent),
      requestId_(requestId),
      command_(command),
      workingDirectory_(workingDirectory),
      timeoutSeconds_(qBound(kMinTimeoutSeconds, timeoutSeconds, kMaxTimeoutSeconds)),
      callbacks_(std::move(callbacks)) {}

PowerShellRunner::~PowerShellRunner() {
    stopProcessAndRemoveScript();
}

bool PowerShellRunner::start(QString *error) {
    // 脚本不落在系统临时目录：跟着程序目录走（data	mp），绿色目录删掉就什么都不留。
    scriptPath_ = QDir(yiuk::vision::programDataPath(QStringLiteral("tmp")))
                      .filePath(QStringLiteral("yiuk-") + requestId_ + QStringLiteral(".ps1"));
    if (!writeUtf8PowerShellScript(scriptPath_, command_, error)) return false;

    const QString executablePath = findPowerShellExecutable();
    if (executablePath.isEmpty()) {
        if (error) *error = QStringLiteral("未找到 PowerShell 可执行程序");
        stopProcessAndRemoveScript();
        return false;
    }

    process_ = new QProcess(this);
    process_->setProcessChannelMode(QProcess::SeparateChannels);
    process_->setProgram(executablePath);
    process_->setArguments({"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
                            "-File", scriptPath_});
#ifdef Q_OS_WIN
    // 没有 CREATE_NO_WINDOW 时会闪出 conhost 并抢走前台激活：
    // 紧接着 SendInput 注入的按键/点击就落到控制台窗口，而不是目标应用。
    process_->setCreateProcessArgumentsModifier([](QProcess::CreateProcessArguments *args) {
        args->flags |= CREATE_NO_WINDOW;
    });
#endif
    if (QDir(workingDirectory_).exists()) process_->setWorkingDirectory(workingDirectory_);
    // 脚本里的临时产物（截图、导出）也放程序目录：scripts.go 优先读这个变量。
    QProcessEnvironment processEnvironment = QProcessEnvironment::systemEnvironment();
    processEnvironment.insert(QStringLiteral("YIUK_DATA_DIR"), yiuk::vision::programDataPath(QString()));
    process_->setProcessEnvironment(processEnvironment);

    connect(process_, &QProcess::readyReadStandardOutput, this, [this] { readAndLimitOutput(false); });
    connect(process_, &QProcess::readyReadStandardError, this, [this] { readAndLimitOutput(true); });
    connect(process_, qOverload<int, QProcess::ExitStatus>(&QProcess::finished), this,
            [this](int exitCode, QProcess::ExitStatus exitStatus) {
                readAndLimitOutput(false);
                readAndLimitOutput(true);
                if (finished_) return;
                const bool success = exitStatus == QProcess::NormalExit && exitCode == 0;
                finishAndReport(success ? QStringLiteral("succeeded") : QStringLiteral("failed"), exitCode, QString());
            });
    connect(process_, &QProcess::errorOccurred, this, [this](QProcess::ProcessError) {
        if (finished_ || !process_ || process_->state() != QProcess::NotRunning) return;
        finishAndReport(QStringLiteral("failed"), -1, process_->errorString());
    });
    QTimer::singleShot(timeoutSeconds_ * 1000, this, [this] {
        if (finished_) return;
        finishAndReport(QStringLiteral("timed_out"), -1, QString());
    });

    process_->start();
    return true;
}

void PowerShellRunner::cancel() {
    if (finished_) return;
    finishAndReport(QStringLiteral("cancelled"), -1, QString());
}

void PowerShellRunner::readAndLimitOutput(bool isStandardError) {
    if (!process_) return;
    QByteArray outputBytes = isStandardError ? process_->readAllStandardError() : process_->readAllStandardOutput();
    if (outputBytes.isEmpty()) return;

    qint64 &retainedBytes = isStandardError ? stderrRetainedBytes_ : stdoutRetainedBytes_;
    bool &truncated = isStandardError ? stderrTruncated_ : stdoutTruncated_;
    const qint64 remainingOutputBytes = qMax<qint64>(0, kOutputLimitBytesPerStream - retainedBytes);
    if (outputBytes.size() > remainingOutputBytes) {
        outputBytes.truncate(static_cast<int>(remainingOutputBytes));
        truncated = true;
    }
    retainedBytes += outputBytes.size();
    if (callbacks_.onOutput) callbacks_.onOutput(outputBytes, isStandardError, truncated);
}

void PowerShellRunner::finishAndReport(const QString &status, int exitCode, const QString &error) {
    if (finished_) return;
    finished_ = true;
    // stopProcessAndRemoveScript() 先断开进程信号，避免终止进程时重入 finishAndReport()。
    stopProcessAndRemoveScript();
    if (callbacks_.onFinished) callbacks_.onFinished(status, exitCode, error);
}

void PowerShellRunner::stopProcessAndRemoveScript() {
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
