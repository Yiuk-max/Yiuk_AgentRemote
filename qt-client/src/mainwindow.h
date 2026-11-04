#pragma once

#include "activitypanel.h"
#include "localbridge.h"
#include "platformclient.h"
#include "securestore.h"
#include "apphelpers.h"
#include "glyph.h"

#include <QApplication>
#include <QCheckBox>
#include <QCloseEvent>
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
    void closeEvent(QCloseEvent *event) override;
private:
    QString deviceCode = loadOrCreateDeviceCode();
    QString temporaryPassword = generateTemporaryPassword(32);
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
    QString pendingDeviceLookupCorrelationId;
    QString pendingDeviceLookupDeviceId;
    QHash<QString, quint64> nextPlatformOutputSequenceByRequest;
    QHash<QString, QString> platformSessionByRequest;
    QHash<QString, QJsonObject> sessions;
    QHash<QString, QJsonObject> tasks;
    // 每条执行中的命令对应一个运行器；运行器负责管理 QProcess 和临时脚本文件
    QHash<QString, PowerShellRunner *> commandRunners;
    QHash<QString, bool> activeFileDownloads;
    struct FileUploadState {
        QString sessionId;
        QString requestId;
        QString targetPath;
        QString temporaryPath;
        QString fileName;
        QFile *file = nullptr;
        QCryptographicHash *checksum = nullptr;
        quint64 expectedSequence = 1;
        qint64 receivedBytes = 0;
        qint64 expectedBytes = 0;
        QString expectedSha256;
        bool overwrite = false;
    };
    // 接收操作端上传并写入磁盘：每个进行中的上传使用一个已打开的临时文件，完成后再重命名到目标位置
    QHash<QString, QSharedPointer<FileUploadState>> activeFileUploads;
    QSystemTrayIcon *trayIcon = nullptr;
    QStatusBar *statusFooter = nullptr;
    QHash<QString, QString> notificationSessions;
    // 平台连接曾经可用时为 true，用于区分连接断开与尚未连接的初始状态
    bool platformWasConnected = false;
    // 远程会话日志保存在面板进程中，所有者可随时关闭或重新打开面板
    // 面板是独立窗口，此类中的任何操作都不能主动将它置于前台
    ActivityPanel *activityPanel = nullptr;
    QHash<QString, QVector<ActivityEntry>> activityLogs;
    void setupSystemTray();
    void showMainWindow();
    bool hasOtherLiveSession(const QString &exceptId = QString()) const;
    // 只判断会话是否满足本机并发限制，不保存或批准会话。
    bool canAdmitPlatformSession(const QJsonObject &session) const;
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
    // 主页上的收藏星标：设备代码尚未收藏时显示描边，收藏后显示琥珀色实心图标
    void refreshFavoriteState();
    void refreshSessionsPage();
    QString sessionConnectorCode(const QJsonObject &session) const;
    QString sessionTargetCode(const QJsonObject &session) const;
    static QJsonObject localToolSuccessResult(const QJsonValue &data = {});
    static QJsonObject localToolErrorResult(const QString &code, const QString &message);
    QString deviceId() const;
    QJsonObject deviceRecord() const;
    void startLocalBridge();
    QString platformConfigPath() const;
    void loadPlatformConfig();
    void savePlatformConfig();
    QString mcpExecutablePath() const;
    QString mcpAgentConfigText() const;
    bool writeMcpPlatformClientConfig(const PlatformClient::Config &platformConfig);
    void copyMcpAgentConfig();
    void initializePlatformClient();
    void updatePlatformState(const QString &state);
    void lookupDeviceBeforeConnecting(const QString &deviceId);
    void promptAndRequestConnection(const QString &deviceId);
    void onDeviceListReceived(const QJsonArray &devices, const QString &correlationId);
    void onPlatformRequestFailed(const QString &correlationId, const QString &code, const QString &message);
    void storeIncomingDeviceSession(const QJsonObject &session, bool approved);
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
        QCryptographicHash *checksum = nullptr;
        quint64 nextSequence = 1;
        qint64 sentBytes = 0;
        qint64 expectedBytes = 0;
    };
    bool sessionAllowsFileRead(const QString &sessionId);
    bool sessionAllowsFileWrite(const QString &sessionId);
    void reportDownloadResultAndCleanup(const QSharedPointer<FileDownloadState> &transferState,
                                        const QString &status, bool resultKnown);
    void sendNextDownloadChunk(const QSharedPointer<FileDownloadState> &transferState);
    void onPlatformFileDownloadRequested(const QString &sessionId, const QString &requestId,
                                         const QString &remotePath);
    void onPlatformFileUploadRequested(const QString &sessionId, const QString &requestId,
                                       const QString &remotePath, const QString &fileName,
                                       qint64 fileSize, const QString &sha256, bool overwrite);
    void onPlatformFileUploadChunk(const QString &sessionId, const QString &requestId,
                                   quint64 sequence, const QString &base64Data, bool eof);
    void reportUploadResultAndCleanup(const QSharedPointer<FileUploadState> &transferState,
                                      const QString &status);
    void discardSessionUploads(const QString &sessionId);
    void onPlatformCommandRequested(const QString &sessionId, const QString &requestId, const QString &command,
                                    const QString &workingDirectory, int timeoutSeconds);
    void startPlatformCommand(const QString &sessionId, const QString &requestId, const QString &command,
                                const QString &workingDirectory, int timeoutSeconds);
    QJsonObject invokeLocalTool(const QString &tool, const QJsonObject &input);
    QJsonObject createLocalSessionRequest(const QJsonObject &input);
    QJsonObject localSessionStatusResult(const QString &sessionId);
    void expireSessionIfNeeded(const QString &sessionId);
    void cancelSessionCommands(const QString &sessionId, const QString &status);
    void refreshPendingRequests();
    void showApprovalDialog(const QString &sessionId);
    void applySessionDecision(const QString &sessionId, bool approved);
    void refreshActiveSessions();
    // 将所有平台会话标记为中断
    // 平台连接断开时调用，避免界面继续显示会话正在运行
    void interruptPlatformSessions();
    QString activeTaskSummary(const QString &sessionId) const;
    QJsonObject downloadLocalFile(const QJsonObject &input);
    QJsonObject uploadLocalFile(const QJsonObject &input);
    QJsonObject startLocalCommand(const QJsonObject &input);
    QJsonObject localCommandResultFromCursor(const QJsonObject &input) const;
    QJsonObject cancelLocalCommand(const QJsonObject &input);
    QJsonObject closeSessionAndStopTasks(const QString &sessionId);
    void appendActivity(const QString &sessionId, const QString &message,
                        ActivityKind kind = ActivityKind::Info);
    QString sessionStatusText(const QString &sessionId) const;
    QString preferredActivitySessionId() const;
    QString activitySessionLabel(const QString &sessionId) const;
    QString activitySessionState(const QString &sessionId) const;
    void refreshOpenActivityPanel();
    void openOrActivateActivityPanel(const QString &sessionId = QString());
    void copyToClipboard(const QString &text, const QString &message);
    void showStatusMessage(const QString &message, int timeout = 0);
};
