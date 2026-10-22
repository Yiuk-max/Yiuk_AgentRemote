#pragma once

#include "activitypanel.h"
#include "localbridge.h"
#include "platformclient.h"
#include "securestore.h"
#include "apphelpers.h"
#include "glyph.h"

#include <QApplication>
#include <QCheckBox>
#include <QClipboard>
#include <QComboBox>
#include <QFrame>
#include <QHash>
#include <QHBoxLayout>
#include <QLabel>
#include <QIcon>
#include <QLineEdit>
#include <QMainWindow>
#include <QMouseEvent>
#include <QPainter>
#include <QPainterPath>
#include <QtMath>
#include <QStandardPaths>
#include <QSettings>
#include <QStackedWidget>
#include <QTemporaryFile>
#include <QTextCursor>
#include <QJsonDocument>
#include <QJsonObject>
#include <QJsonArray>
#include <QUuid>
#include <QPushButton>
#include <QRandomGenerator>
#include <QStatusBar>
#include <QStyle>
#include <QSysInfo>
#include <QSystemTrayIcon>
#include <QMenu>
#include <QVBoxLayout>
#include <QWidget>
#include <QWindow>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QCryptographicHash>
#include <QSharedPointer>
#include <QDateTime>
#include <QDialog>
#include <QScrollArea>
#include <QSaveFile>
#include <QTimer>

#ifndef YIUK_CLIENT_VERSION
#define YIUK_CLIENT_VERSION "dev"
#endif

class PowerShellRunner;

class MainWindow final : public QMainWindow {
public:
    MainWindow();
protected:
    bool eventFilter(QObject *watched, QEvent *event) override;
private:
    QString deviceCode = loadOrCreateDeviceCode();
    QString temporaryPassword = randomPassword(32);
    int passwordVersion = 1;
    QLabel *passwordValue = nullptr;
    QPushButton *togglePassword = nullptr;
    bool passwordVisible = false;
    QLineEdit *remoteCode = nullptr;
    QPushButton *favoriteButton = nullptr;
    QStackedWidget *pages = nullptr;
    QPushButton *navHome = nullptr;
    QPushButton *navDevices = nullptr;
    QPushButton *navHistory = nullptr;
    QPushButton *navSettings = nullptr;
    QLabel *deviceIdValue = nullptr;
    QLabel *sessionSummaryValue = nullptr;
    QVBoxLayout *deviceListLayout = nullptr;
    QStringList savedDevices;
    QVBoxLayout *sessionsListLayout = nullptr;
    QWidget *homePage = nullptr;
    QWidget *devicesPage = nullptr;
    QWidget *sessionsPage = nullptr;
    QWidget *settingsPage = nullptr;
    QLabel *serviceState = nullptr;
    QWidget *pendingPanel = nullptr;
    QVBoxLayout *pendingLayout = nullptr;
    QWidget *activePanel = nullptr;
    QVBoxLayout *activeLayout = nullptr;
    QLabel *platformState = nullptr;
    QLabel *mcpState = nullptr;
    QLabel *deviceState = nullptr;
    QTimer *sessionTimer = nullptr;
    LocalBridge *bridge = nullptr;
    PlatformClient *platform = nullptr;
    QLineEdit *platformAddress = nullptr;
    QLineEdit *platformPassword = nullptr;
    QCheckBox *platformTls = nullptr;
    QComboBox *platformMode = nullptr;
    QLabel *platformStatusValue = nullptr;
    QPushButton *platformConnectButton = nullptr;
    QString pendingDeviceLookupId;
    QString pendingDeviceLookupCode;
    QHash<QString, quint64> platformOutputSequence;
    QHash<QString, QString> platformTaskSession;
    QHash<QString, QJsonObject> sessions;
    QHash<QString, QJsonObject> tasks;
    // One runner per in-flight command; the runner owns the QProcess and the
    // temporary script file.
    QHash<QString, PowerShellRunner *> runs;
    QHash<QString, bool> activeFileDownloads;
    QSystemTrayIcon *trayIcon = nullptr;
    QStatusBar *statusFooter = nullptr;
    QHash<QString, QString> notificationSessions;
    // The remote-session log is kept for the panel process, which the owner can
    // close and reopen at any time. The panel is an independent window; nothing
    // in this class may raise it on its own.
    ActivityPanel *activityPanel = nullptr;
    QHash<QString, QVector<ActivityEntry>> activityLogs;
    void setupSystemTray();
    static QString loadOrCreateDeviceCode();
    QWidget *createSidebar();
    QPushButton *navButton(const QString &text, Glyph glyph, bool active = false);
    QPushButton *iconButton(Glyph glyph, const QString &tooltip);
    QString sessionStatePath() const;
    void loadSessionState();
    void persistSessionState();
    QWidget *createHomePage();
    QWidget *pageShell(const QString &title, const QString &subtitle, QVBoxLayout **outLayout);
    QWidget *deviceRow(const QString &code, const QString &name, bool local);
    QWidget *createDevicesPage();
    QWidget *createSessionsPage();
    QWidget *createSettingsPage();
    void showPage(int index);
    void refreshDevicesPage();
    // Star on the home page: outlined when the typed device code is not saved
    // yet, filled amber once it is.
    void refreshFavoriteState();
    void refreshSessionsPage();
    QString sessionConnectorCode(const QJsonObject &session) const;
    QString sessionTargetCode(const QJsonObject &session) const;
    static QJsonObject ok(const QJsonValue &data = {});
    static QJsonObject fail(const QString &code, const QString &message);
    QString deviceId() const;
    QJsonObject deviceRecord() const;
    void startLocalBridge();
    QString platformConfigPath() const;
    void loadPlatformConfig();
    void savePlatformConfig();
    QString mcpToolSourcePath() const;
    QString mcpAgentConfigText() const;
    bool syncMcpClientConfig(const PlatformClient::Config &cfg);
    void copyMcpAgentConfig();
    void startPlatform();
    void updatePlatformState(const QString &state);
    void lookupDeviceBeforeConnecting(const QString &deviceId);
    void promptAndRequestConnection(const QString &deviceId);
    void onDeviceListReceived(const QJsonArray &devices, const QString &requestId);
    void onPlatformRequestFailed(const QString &requestId, const QString &code, const QString &message);
    void adoptDeviceSession(const QJsonObject &session, bool approved);
    void onPlatformSessionRequested(const QJsonObject &session);
    void onPlatformSessionUpdated(const QJsonObject &session);
    void onPlatformSessionCreated(const QJsonObject &session);
    void onPlatformCloseRequested(const QString &sessionId);
    void onPlatformCancelRequested(const QString &sessionId, const QString &requestId);
    struct FileDownloadState {
        QString sessionId;
        QString requestId;
        QString fileName;
        QFile *file = nullptr;
        QCryptographicHash *hash = nullptr;
        quint64 sequence = 1;
        qint64 sent = 0;
        qint64 expected = 0;
    };
    bool sessionAllowsFileRead(const QString &sessionId);
    void finishFileDownload(const QSharedPointer<FileDownloadState> &state, const QString &status,
                            bool resultKnown);
    void pumpFileDownload(const QSharedPointer<FileDownloadState> &state);
    void onPlatformFileDownloadRequested(const QString &sessionId, const QString &requestId,
                                         const QString &remotePath);
    void onPlatformCommandRequested(const QString &sessionId, const QString &requestId, const QString &command,
                                    const QString &workingDirectory, int timeoutSeconds);
    void executePlatformCommand(const QString &sessionId, const QString &requestId, const QString &command,
                                const QString &workingDirectory, int timeoutSeconds);
    QJsonObject invokeLocalTool(const QString &tool, const QJsonObject &input);
    QJsonObject requestLocalSession(const QJsonObject &input);
    QJsonObject sessionStatus(const QString &id);
    void expireSessionIfNeeded(const QString &id);
    void stopTasksForSession(const QString &id, const QString &status);
    void refreshPendingRequests();
    void showApprovalDialog(const QString &id);
    void decideSession(const QString &id, bool approved);
    void refreshActiveSessions();
    QString activeTaskSummary(const QString &sessionId) const;
    QJsonObject downloadLocalFile(const QJsonObject &input);
    QJsonObject executeLocalCommand(const QJsonObject &input);
    QJsonObject commandResult(const QJsonObject &input) const;
    QJsonObject cancelCommand(const QJsonObject &input);
    QJsonObject closeSession(const QString &id);
    void appendActivity(const QString &sessionId, const QString &message,
                        ActivityKind kind = ActivityKind::Info);
    QString sessionStatusText(const QString &sessionId) const;
    QString currentActivitySession() const;
    QString activitySessionLabel(const QString &sessionId) const;
    QString activitySessionState(const QString &sessionId) const;
    void syncActivityPanel();
    void showActivityWindow(const QString &sessionId = QString());
    void copyToClipboard(const QString &text, const QString &message);
    void showStatusMessage(const QString &message, int timeout = 0);
};
