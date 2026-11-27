#include "mainwindow.h"

bool MainWindow::allowsUserQuestion(const QString &sessionId) {
    expireSessionIfNeeded(sessionId);
    const auto session = sessions.value(sessionId);
    const auto status = session.value("status").toString();
    if (status != "active" && status != "approved") return false;
    return session.value("permissions").toArray().contains(QStringLiteral("execute_command"));
}

QJsonObject MainWindow::askLocalUser(const QJsonObject &input) {
    const auto sessionId = input.value("session_id").toString();
    if (!allowsUserQuestion(sessionId) || sessions.value(sessionId).value("source").toString().startsWith("platform"))
        return localToolErrorResult("PERMISSION_DENIED", "只能在已批准的本地会话中询问用户");
    int timeout = 120;
    if (input.contains("timeout_seconds")) {
        const auto value = input.value("timeout_seconds");
        timeout = value.toInt(-1);
        if (!value.isDouble() || value.toDouble() != timeout || timeout < 15 || timeout > 300)
            return localToolErrorResult("INVALID_ARGUMENT", "timeout_seconds 必须在 15–300 之间");
    }
    QJsonObject request = input;
    request["request_id"] = QUuid::createUuid().toString(QUuid::WithoutBraces);
    const qint64 sessionEnd = sessions.value(sessionId).value("expires_at_ms").toInteger() / 1000;
    request["expires_at"] = qMin(QDateTime::currentSecsSinceEpoch() + timeout, sessionEnd);
    const auto error = userQuestion->ask(request);
    if (!error.isEmpty()) return localToolErrorResult(error, "无法发起询问");
    appendActivity(sessionId, QStringLiteral("Agent 发起用户询问"));
    return localToolSuccessResult(userQuestion->result(sessionId, request.value("request_id").toString()));
}

QJsonObject MainWindow::getLocalUserReply(const QJsonObject &input) {
    const auto sessionId = input.value("session_id").toString();
    if (!sessions.contains(sessionId) || sessions.value(sessionId).value("source").toString().startsWith("platform"))
        return localToolErrorResult("SESSION_NOT_FOUND", "本地会话不存在");
    const auto reply = userQuestion->result(sessionId, input.value("request_id").toString());
    if (reply.isEmpty()) return localToolErrorResult("QUESTION_NOT_FOUND", "询问不存在或已清理");
    return localToolSuccessResult(reply);
}
