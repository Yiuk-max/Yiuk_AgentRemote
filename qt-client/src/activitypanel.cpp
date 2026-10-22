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
        case ActivityKind::Success: return QStringLiteral("ok");
        case ActivityKind::Info:
        default: return QStringLiteral("info");
    }
}

// One line per message: newlines and backslashes are escaped, and the pipe is
// safe because the panel only splits the first fields of a line.
QString escapeField(const QString &text) {
    QString out;
    out.reserve(text.size() + 8);
    for (const QChar ch : text) {
        if (ch == QLatin1Char('\\')) out += QLatin1String("\\\\");
        else if (ch == QLatin1Char('\n')) out += QLatin1String("\\n");
        else if (ch == QLatin1Char('\r')) continue;
        else out += ch;
    }
    return out;
}

QString entryLine(const ActivityEntry &entry) {
    return QStringLiteral("E|%1|%2|%3")
        .arg(kindToken(entry.kind), escapeField(entry.time), escapeField(entry.text));
}

QString panelExecutable() {
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
        const QString executable = panelExecutable();
        if (!QFileInfo::exists(executable)) return false;
        process = new QProcess(this);
        process->setProgram(executable);
        process->setArguments({QStringLiteral("--title"), title,
                               QStringLiteral("--label"), label,
                               QStringLiteral("--state"), state,
                               QStringLiteral("--status"), status});
        // stdout/stderr are unused by the panel; stdin carries the protocol.
        process->setProcessChannelMode(QProcess::MergedChannels);
        connect(process, &QProcess::finished, this, [this] { cleanup(); });
        connect(process, &QProcess::errorOccurred, this, [this](QProcess::ProcessError error) {
            if (error == QProcess::FailedToStart) cleanup();
        });
        process->start();
        if (!process->waitForStarted(4000)) {
            cleanup();
            return false;
        }
    }
    reset(label, state, status, history);
    return true;
}

void ActivityPanel::reset(const QString &label, const QString &state, const QString &status,
                          const QVector<ActivityEntry> &history) {
    lastState = state;
    lastStatus = status;
    send(QStringLiteral("H|%1|%2|%3|%4")
             .arg(escapeField(QStringLiteral("远程协助活动")), escapeField(label), state,
                  escapeField(status)));
    send(QStringLiteral("R|"));
    for (const ActivityEntry &entry : history) send(entryLine(entry));
}

void ActivityPanel::append(const ActivityEntry &entry) {
    send(entryLine(entry));
}

void ActivityPanel::setStatus(const QString &state, const QString &status) {
    if (state == lastState && status == lastStatus) return;
    lastState = state;
    lastStatus = status;
    send(QStringLiteral("S|%1|%2").arg(state, escapeField(status)));
}

void ActivityPanel::activate() {
    send(QStringLiteral("A|"));
}

void ActivityPanel::send(const QString &line) {
    if (!process || process->state() == QProcess::NotRunning) return;
    process->write(line.toUtf8());
    process->write("\n", 1);
}

void ActivityPanel::close() {
    if (!process) return;
    // Detach first: waiting below pumps the event loop, and the finished handler
    // must not clear the pointer while this function still uses it.
    QProcess *target = process;
    process = nullptr;
    lastState.clear();
    lastStatus.clear();
    target->disconnect(this);
    target->closeWriteChannel();
    if (!target->waitForFinished(1500)) {
        target->kill();
        target->waitForFinished(400);
    }
    target->deleteLater();
}

void ActivityPanel::cleanup() {
    if (!process) return;
    QProcess *finished = process;
    process = nullptr;
    finished->deleteLater();
    lastState.clear();
    lastStatus.clear();
}
