#include "activitypanel.h"

#include <QCoreApplication>
#include <QDir>
#include <QFileInfo>
#include <QProcess>

namespace {

QString kindToken(ActivityKind kind) {
    switch (kind) {
        case ActivityKind::Session: return QStringLiteral("session");
        case ActivityKind::Execute: return QStringLiteral("exec");
        case ActivityKind::Output: return QStringLiteral("out");
        case ActivityKind::Error: return QStringLiteral("err");
        case ActivityKind::Read: return QStringLiteral("read");
        case ActivityKind::Download: return QStringLiteral("file");
        case ActivityKind::Upload: return QStringLiteral("upload");
        case ActivityKind::Success: return QStringLiteral("ok");
        case ActivityKind::Info:
        default: return QStringLiteral("info");
    }
}

// 每条消息占一行：换行符和反斜杠会转义；面板只拆分每行的前几个字段，因此管道符无需转义
QString escapeField(const QString &text) {
    QString escapedText;
    escapedText.reserve(text.size() + 8);
    for (const QChar ch : text) {
        if (ch == QLatin1Char('\\')) escapedText += QLatin1String("\\\\");
        else if (ch == QLatin1Char('\n')) escapedText += QLatin1String("\\n");
        else if (ch == QLatin1Char('\r')) continue;
        else escapedText += ch;
    }
    return escapedText;
}

QString serializeActivityEntry(const ActivityEntry &entry) {
    return QStringLiteral("E|%1|%2|%3")
        .arg(kindToken(entry.kind), escapeField(entry.time), escapeField(entry.text));
}

QString panelExecutablePath() {
    return QDir(QCoreApplication::applicationDirPath()).filePath(QStringLiteral("yiuk-activity-panel.exe"));
}

}  // namespace

ActivityPanel::ActivityPanel(QObject *parent) : QObject(parent) {}

ActivityPanel::~ActivityPanel() {
    close();
}

bool ActivityPanel::isOpen() const {
    return process && process->state() != QProcess::NotRunning;
}

bool ActivityPanel::open(const QString &title, const QString &label, const QString &state,
                         const QString &status, const QVector<ActivityEntry> &history) {
    if (!isOpen()) {
        const QString executable = panelExecutablePath();
        if (!QFileInfo::exists(executable)) return false;
        process = new QProcess(this);
        process->setProgram(executable);
        process->setArguments({QStringLiteral("--title"), title,
                               QStringLiteral("--label"), label,
                               QStringLiteral("--state"), state,
                               QStringLiteral("--status"), status});
        // 面板不使用 stdout/stderr；stdin 用于传递协议数据
        process->setProcessChannelMode(QProcess::MergedChannels);
        connect(process, &QProcess::finished, this, [this] { releaseFinishedProcess(); });
        connect(process, &QProcess::errorOccurred, this, [this](QProcess::ProcessError error) {
            if (error == QProcess::FailedToStart) releaseFinishedProcess();
        });
        process->start();
        if (!process->waitForStarted(4000)) {
            releaseFinishedProcess();
            return false;
        }
    }
    replaceHistoryAndStatus(label, state, status, history);
    return true;
}

void ActivityPanel::replaceHistoryAndStatus(const QString &label, const QString &state, const QString &status,
                          const QVector<ActivityEntry> &history) {
    lastSessionState = state;
    lastStatusText = status;
    writeProtocolLine(QStringLiteral("H|%1|%2|%3|%4")
             .arg(escapeField(QStringLiteral("远程协助活动")), escapeField(label), state,
                  escapeField(status)));
    writeProtocolLine(QStringLiteral("R|"));
    for (const ActivityEntry &entry : history) writeProtocolLine(serializeActivityEntry(entry));
}

void ActivityPanel::append(const ActivityEntry &entry) {
    writeProtocolLine(serializeActivityEntry(entry));
}

void ActivityPanel::setStatus(const QString &state, const QString &status) {
    if (state == lastSessionState && status == lastStatusText) return;
    lastSessionState = state;
    lastStatusText = status;
    writeProtocolLine(QStringLiteral("S|%1|%2").arg(state, escapeField(status)));
}

void ActivityPanel::activate() {
    writeProtocolLine(QStringLiteral("A|"));
}

void ActivityPanel::writeProtocolLine(const QString &line) {
    if (!process || process->state() == QProcess::NotRunning) return;
    process->write(line.toUtf8());
    process->write("\n", 1);
}

void ActivityPanel::close() {
    if (!process) return;
    // 先解除关联：下面的等待会处理事件循环，完成处理器不能在当前函数仍使用该指针时将其清空
    QProcess *closingProcess = process;
    process = nullptr;
    lastSessionState.clear();
    lastStatusText.clear();
    closingProcess->disconnect(this);
    closingProcess->closeWriteChannel();
    if (!closingProcess->waitForFinished(1500)) {
        closingProcess->kill();
        closingProcess->waitForFinished(400);
    }
    closingProcess->deleteLater();
}

void ActivityPanel::releaseFinishedProcess() {
    if (!process) return;
    QProcess *finishedProcess = process;
    process = nullptr;
    finishedProcess->deleteLater();
    lastSessionState.clear();
    lastStatusText.clear();
}
