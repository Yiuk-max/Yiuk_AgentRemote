#pragma once

#include "activitypanel.h"
#include "localbridge.h"
#include "platformclient.h"
#include "securestore.h"
#include "apphelpers.h"
#include "glyph.h"
#include "userquestion.h"

#include <QApplication>
#include <QCheckBox>
#include <QCloseEvent>
#include <QClipboard>
#include <QComboBox>
#include <QFrame>
#include <QHash>
#include <QSet>
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

#include <atomic>

#ifndef YIUK_CLIENT_VERSION
#define YIUK_CLIENT_VERSION "dev"
#endif

class PowerShellRunner;
class NativeOperationDispatcher;
namespace yiuk::vision {
class VisionService;
}

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
    UserQuestion *userQuestion = nullptr;
    QSet<QString> platformQuestionIds;
    QJsonObject askLocalUser(const QJsonObject &input);
    QJsonObject getLocalUserReply(const QJsonObject &input);
    bool allowsUserQuestion(const QString &sessionId);
    QLineEdit *platformAddress = nullptr;
    QLineEdit *platformPassword = nullptr;
    QCheckBox *platformTls = nullptr;
    QPushButton *platformConnectButton = nullptr;
    QString pendingDeviceLookupCorrelationId;
    QString pendingDeviceLookupDeviceId;
    QHash<QString, quint64> nextPlatformOutputSequenceByRequest;
    QHash<QString, QString> platformSessionByRequest;
    QHash<QString, QJsonObject> sessions;
    QHash<QString, QJsonObject> tasks;
    // 每条执行中的命令对应一个运行器；运行器负责管理 QProcess 和临时脚本文件
    QHash<QString, PowerShellRunner *> commandRunners;
    // 原生操作分发器：注册了哪些操作就上报哪些能力（见 platformlink.cpp 的注册 message）。
    NativeOperationDispatcher *nativeOps = nullptr;
    // 抓屏 / 视觉重操作的唯一入口：会话级互斥、最小间隔、快照与临时文件。
    yiuk::vision::VisionService *vision = nullptr;
    QSet<QString> nativeRequestIds;
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
    void refreshSessionsPage();
    QString sessionConnectorCode(const QJsonObject &session) const;
    QString sessionTargetCode(const QJsonObject &session) const;
    static QJsonObject localToolSuccessResult(const QJsonValue &data = {});
    static QJsonObject localToolErrorResult(const QString &code, const QString &message);
    QString deviceId() const;
    static bool saveDeviceCode(const QString &digits);
    void onDeviceCodeAssigned(const QString &deviceId);
    QJsonObject deviceRecord() const;
    void startLocalBridge();
    void loadPlatformConfig();
    void savePlatformConfig();
    QString mcpExecutablePath() const;
    QString mcpSetupPromptText() const;
    bool writeMcpPlatformClientConfig(const PlatformClient::Config &platformConfig);
    void copyMcpSetupPrompt();
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
    // 原生操作（抓屏 / 鼠标 / 视觉定位）：由 NativeOperationDispatcher 在工作线程执行，
    // 结果回到主线程后走 command_output + command_result 回给平台。
    void onPlatformNativeRequested(const QString &sessionId, const QString &requestId, const QString &operation,
                                   const QJsonObject &arguments, int timeoutSeconds);
    void setupNativeOperations();
    // 原生操作 vision.find：文本 / 图像定位，返回最近截图的坐标与 screenshot_id。
    QJsonObject runVisionFind(const QString &sessionId, const QJsonObject &arguments, const std::atomic_bool *cancelled);
    // 原生操作 vision.click_target：客户端内 find -> pick -> click 一笔事务。
    QJsonObject runVisionClickTarget(const QString &sessionId, const QJsonObject &arguments,
                                     const std::atomic_bool *cancelled);
    // 原生操作 vision.save_template：从最近一帧的保留像素裁剪模板。
    QJsonObject runVisionSaveTemplate(const QString &sessionId, const QJsonObject &arguments,
                                      const std::atomic_bool *cancelled);
    void sendNativeEnvelope(const QString &sessionId, const QString &requestId, const QJsonObject &envelope);
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
    // /native：原生操作的异步入口（会话校验 + 走 NativeOperationDispatcher + 回信封）。
    void runLocalNative(const QJsonObject &request, const std::function<void(const QJsonObject &)> &respond);
    // 下面这几个是本机 bridge 的工具实现。它们看起来属于一个独立的 LocalToolHost，
    // 但实际上要动到 18 个 MainWindow 成员（sessions/tasks/commandRunners、托盘、
    // 审批弹窗、会话持久化、状态栏……）——抽类的代价是一个很肥的 host 接口，
    // 而拆分后的收益只是头文件短几行。所以只把分发换成了 invokeLocalTool 里的表，
    // 工具语义仍留在这里；真要拆，先给会话/任务状态找一个能独立于 UI 的宿主。
    QJsonObject localListDevices(const QJsonObject &input);
    QJsonObject localDeviceStatus(const QJsonObject &input);
    QJsonObject localCloseSession(const QJsonObject &input);
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
    QJsonObject localCommandResultFromCursor(const QJsonObject &input);
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
