#pragma once

#include <QObject>
#include <QString>
#include <QVector>

class QProcess;

// What the panel renders: the kind decides the ink (text colour) of the entry.
// It mirrors yiuk::Kind in src/panel/paneltypes.h; the wire tokens in
// activitypanel.cpp keep the two processes in sync.
enum class ActivityKind {
    Info,
    Session,
    Execute,
    Output,
    Error,
    Read,
    Download,
    Success,
};

struct ActivityEntry {
    ActivityKind kind = ActivityKind::Info;
    QString time;  // "HH:MM:SS"
    QString text;  // may contain '\n'
};

// The receive-only activity log of the current session lives in its own process
// (yiuk-activity-panel.exe): a native, per-pixel translucent Win32 window the
// owner may close at any time without touching the session. This class only owns
// that child and streams protocol lines to its stdin.
class ActivityPanel final : public QObject {
public:
    explicit ActivityPanel(QObject *parent = nullptr);
    ~ActivityPanel() override;

    bool isOpen() const;

    // Starts the panel when it is not running and replays `history`; returns
    // false when the companion executable is missing.
    bool open(const QString &title, const QString &label, const QString &state, const QString &status,
              const QVector<ActivityEntry> &history);

    // Replaces the panel content (new current session) and status.
    void reset(const QString &label, const QString &state, const QString &status,
               const QVector<ActivityEntry> &history);

    void append(const ActivityEntry &entry);
    void setStatus(const QString &state, const QString &status);

    // Only ever called because the user asked for the window; new activity must
    // never bring it back into the foreground.
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
