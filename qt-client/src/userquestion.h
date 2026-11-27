#pragma once

#include <QObject>
#include <QJsonObject>
#include <QHash>
#include <QPointer>
#include <QTimer>

class QWidget;
class QLabel;
class YiukDialog;

// One ephemeral question window, shared by the platform and local bridge.
// Never writes question/answer text to disk or activity logs.
class UserQuestion final : public QObject {
    Q_OBJECT
public:
    explicit UserQuestion(QWidget *owner);
    // Returns an empty string on success, otherwise a stable error code.
    QString ask(const QJsonObject &request);
    QJsonObject result(const QString &sessionId, const QString &requestId);
    void cancelSession(const QString &sessionId, const QString &status);
    void cancel(const QString &sessionId, const QString &requestId, const QString &status);
    static bool validRequest(const QJsonObject &request);
signals:
    void finished(const QJsonObject &reply);
private:
    void settle(const QString &status, const QString &source = {}, const QString &answer = {}, int index = -1);
    void tick();
    QWidget *owner;
    QPointer<YiukDialog> dialog;
    QHash<QString, QJsonObject> records;
    QString pendingId;
    QTimer timer;
};
