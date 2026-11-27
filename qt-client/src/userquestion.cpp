#include "userquestion.h"
#include "yiukdialog.h"

#include <QApplication>
#include <QCursor>
#include <QDateTime>
#include <QGuiApplication>
#include <QHBoxLayout>
#include <QJsonArray>
#include <QLabel>
#include <QLineEdit>
#include <QPushButton>
#include <QScreen>
#include <QScrollArea>
#include <QSet>
#include <QVBoxLayout>
#include <cmath>

namespace {
int characters(const QString &text) { return text.toUcs4().size(); }

QLabel *plainLabel(const QString &text, QWidget *parent = nullptr) {
    auto *label = new QLabel(text, parent);
    label->setTextFormat(Qt::PlainText);
    label->setWordWrap(true);
    label->setTextInteractionFlags(Qt::TextSelectableByMouse);
    return label;
}

// A whole-row answer button. The button paints the shared dialog style; the
// wrapped label inside it allows long Chinese answers without eliding.
class ChoiceButton final : public QPushButton {
public:
    explicit ChoiceButton(const QString &text, QWidget *parent = nullptr) : QPushButton(parent) {
        setCursor(Qt::PointingHandCursor);
        setSizePolicy(QSizePolicy::Preferred, QSizePolicy::Minimum);
        setAccessibleName(text);
        setStyleSheet("QPushButton { text-align: left; }");
        auto *layout = new QHBoxLayout(this);
        layout->setContentsMargins(12, 9, 12, 9);
        label = new QLabel(text, this);
        label->setTextFormat(Qt::PlainText);
        label->setWordWrap(true);
        label->setAttribute(Qt::WA_TransparentForMouseEvents);
        label->setStyleSheet("color: #263446; font-size: 13px; background: transparent;");
        layout->addWidget(label);
    }
    bool hasHeightForWidth() const override { return true; }
    int heightForWidth(int width) const override {
        return label->heightForWidth(qMax(80, width - 24)) + 18;
    }
    QSize sizeHint() const override {
        const int width = qMax(240, this->width());
        return QSize(width, heightForWidth(width));
    }
    QSize minimumSizeHint() const override { return QSize(200, heightForWidth(200)); }

private:
    QLabel *label = nullptr;
};
}  // namespace

UserQuestion::UserQuestion(QWidget *parent) : QObject(parent), owner(parent) {
    timer.setInterval(250);
    connect(&timer, &QTimer::timeout, this, &UserQuestion::tick);
    timer.start();
}

bool UserQuestion::validRequest(const QJsonObject &r) {
    for (const auto *key : {"session_id", "request_id", "question"})
        if (!r.value(key).isString() || r.value(key).toString().trimmed().isEmpty()) return false;
    if (r.value("session_id").toString().size() > 128 || r.value("request_id").toString().size() > 128 ||
        characters(r.value("question").toString().trimmed()) > 1000) return false;
    if (r.contains("options") && !r.value("options").isArray()) return false;
    const auto options = r.value("options").toArray();
    if (options.size() > 6) return false;
    QSet<QString> seen;
    for (const auto &value : options) {
        const auto text = value.toString().trimmed();
        if (!value.isString() || text.isEmpty() || characters(text) > 100 || seen.contains(text)) return false;
        seen.insert(text);
    }
    const double expires = r.value("expires_at").toDouble();
    const double now = QDateTime::currentSecsSinceEpoch();
    return r.value("expires_at").isDouble() && std::isfinite(expires) && expires > now && expires <= now + 301;
}

QString UserQuestion::ask(const QJsonObject &request) {
    tick();
    if (!validRequest(request)) return "INVALID_ARGUMENT";
    const QString id = request.value("request_id").toString();
    QJsonObject normalized = request;
    normalized["question"] = request.value("question").toString().trimmed();
    QJsonArray options;
    for (const auto &value : request.value("options").toArray()) options.append(value.toString().trimmed());
    normalized["options"] = options;
    if (records.contains(id)) {
        const auto existing = records.value(id);
        return existing.value("session_id") == normalized.value("session_id") &&
            existing.value("question") == normalized.value("question") && existing.value("options") == normalized.value("options")
            ? QString() : QStringLiteral("REQUEST_CONFLICT");
    }
    if (!pendingId.isEmpty()) return "QUESTION_BUSY";
    int count = 0;
    for (const auto &r : records) if (r.value("session_id") == normalized.value("session_id")) ++count;
    // A small bounded per-session history, independent of command output tasks.
    if (count >= 32) return "QUESTION_LIMIT_REACHED";
    normalized["status"] = "pending";
    records.insert(id, normalized);
    pendingId = id;

    auto *popup = new YiukDialog(owner, QStringLiteral("远程协助询问"));
    dialog = popup;
    popup->setObjectName("userQuestionDialog");
    popup->setAttribute(Qt::WA_DeleteOnClose);
    // Windows blocks background apps from stealing focus, and the client is
    // usually in the tray while the owner works elsewhere. Keep this question
    // above other windows so it is actually seen; it closes on answer/decline.
    popup->setWindowFlag(Qt::WindowStaysOnTopHint, true);
    // Non-modal: the owner can still stop the session while the question waits.
    popup->setModal(false);
    // Inherits the shared main-window QSS through the parent; no second skin.

    QScreen *screen = (owner->isVisible() && owner->screen()) ? owner->screen()
                                                             : QGuiApplication::screenAt(QCursor::pos());
    if (!screen) screen = QGuiApplication::primaryScreen();
    const QRect available = screen->availableGeometry();
    popup->setFixedWidth(qMin(500, available.width() - 16));
    popup->ensurePolished();

    // Only the question and answers may scroll; the reply field and buttons
    // always stay visible. Ordinary questions need no scroll area at all, so
    // the dialog stays compact instead of reserving an empty viewport.
    auto *body = new QWidget;
    body->setBackgroundRole(QPalette::Base);
    auto *layout = new QVBoxLayout(body);
    layout->setContentsMargins(0, 0, 0, 0);
    layout->setSpacing(8);

    auto *question = plainLabel(normalized.value("question").toString());
    question->setObjectName("userQuestionText");
    question->setAlignment(Qt::AlignLeft | Qt::AlignTop);
    question->setStyleSheet("font-size: 15px; font-weight: 600; color: #182230;");
    layout->addWidget(question);
    if (!options.isEmpty()) layout->addSpacing(4);
    for (int i = 0; i < options.size(); ++i) {
        const QString text = options.at(i).toString();
        auto *choice = new ChoiceButton(text, body);
        choice->setObjectName(QStringLiteral("userQuestionOption%1").arg(i));
        // Enter belongs to the reply field only; an implicit default answer
        // button would let a stray Enter pick the first option.
        choice->setAutoDefault(false);
        choice->setDefault(false);
        // One click answers: these options are already answers from the Agent.
        connect(choice, &QPushButton::clicked, this, [this, text, i] {
            settle(QStringLiteral("answered"), QStringLiteral("option"), text, i);
        });
        layout->addWidget(choice);
    }
    // Absorb surplus height here, otherwise a single wrapped question label
    // is stretched taller than its text and the dialog looks half empty.
    layout->addStretch(1);
    popup->content()->addWidget(body, 1);
    popup->content()->addSpacing(4);

    auto *edit = new QLineEdit(popup);
    edit->setObjectName("settingsField");
    edit->setAccessibleName(QStringLiteral("直接输入回答"));
    edit->setPlaceholderText(QStringLiteral("输入你的回答…"));
    edit->setMaxLength(2000);  // Unicode characters are validated below.
    popup->content()->addWidget(edit);

    auto *decline = popup->addButton(QStringLiteral("暂不回答"));
    auto *send = popup->addButton(QStringLiteral("发送"), YiukDialog::ButtonKind::Primary);
    send->setEnabled(false);
    send->setAutoDefault(false);
    decline->setAutoDefault(false);
    auto submitText = [this, edit] {
        const QString answer = edit->text().trimmed();
        if (answer.isEmpty() || characters(answer) > 1000) return;
        settle(QStringLiteral("answered"), QStringLiteral("text"), answer);
    };
    connect(edit, &QLineEdit::textChanged, popup, [send](const QString &text) {
        const QString answer = text.trimmed();
        send->setEnabled(!answer.isEmpty() && characters(answer) <= 1000);
    });
    connect(edit, &QLineEdit::returnPressed, popup, [submitText] { submitText(); });
    connect(send, &QPushButton::clicked, popup, submitText);
    connect(decline, &QPushButton::clicked, popup, &QDialog::reject);
    connect(popup, &QDialog::rejected, this, [this, id] { if (pendingId == id) settle(QStringLiteral("cancelled")); });

    popup->content()->invalidate();
    popup->adjustSize();
    const int maxHeight = qMin(available.height() - 16, 620);
    if (popup->height() > maxHeight) {
        // Extremely long question or six wrapped options: scroll the body only.
        popup->content()->removeWidget(body);
        auto *scroll = new QScrollArea(popup);
        scroll->setWidgetResizable(true);
        scroll->setFrameShape(QFrame::NoFrame);
        scroll->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
        scroll->viewport()->setBackgroundRole(QPalette::Base);
        scroll->setWidget(body);
        popup->content()->insertWidget(0, scroll, 1);
        popup->setMaximumHeight(maxHeight);
        popup->adjustSize();
    }
    popup->show();  // Asynchronous: no nested event loop, heartbeats keep running.
    // After the first layout pass the real content width is known; re-run the
    // size pass so wrapped text uses that width instead of a stale minimum.
    popup->content()->invalidate();
    popup->adjustSize();
    const QRect host = owner->isVisible() ? owner->window()->frameGeometry() : QRect();
    const QPoint target = host.isValid() ? host.center() : available.center();
    // Windows adds an invisible resize border around frameless windows, so
    // clamp the frame (not the client size) into the usable screen area.
    const QRect frame = popup->frameGeometry();
    const QPoint frameOffset = frame.topLeft() - popup->geometry().topLeft();
    QRect placed(QPoint(), frame.size());
    placed.moveCenter(target);
    placed.moveLeft(qBound(available.left(), placed.left(), available.right() - placed.width() + 1));
    placed.moveTop(qBound(available.top(), placed.top(), available.bottom() - placed.height() + 1));
    popup->move(placed.topLeft() - frameOffset);
    popup->raise();
    popup->activateWindow();
    QApplication::alert(popup);
    edit->setFocus();
    return {};
}

void UserQuestion::settle(const QString &status, const QString &source, const QString &answer, int index) {
    if (pendingId.isEmpty()) return;
    auto record = records.value(pendingId);
    QString terminal = status;
    if (status == "answered" && QDateTime::currentSecsSinceEpoch() >= record.value("expires_at").toInteger())
        terminal = "timed_out";
    record["status"] = terminal;
    record["finished_at"] = QDateTime::currentSecsSinceEpoch();
    if (terminal == "answered") {
        record["answer_source"] = source;
        record["answer"] = answer;
        if (index >= 0) record["selected_index"] = index;
    }
    records[pendingId] = record;
    pendingId.clear();
    auto popup = dialog;
    dialog = nullptr;
    if (popup) popup->close();
    emit finished(result(record.value("session_id").toString(), record.value("request_id").toString()));
}

QJsonObject UserQuestion::result(const QString &sessionId, const QString &requestId) {
    tick();
    const auto r = records.value(requestId);
    if (r.isEmpty() || r.value("session_id").toString() != sessionId) return {};
    QJsonObject reply;
    for (const auto *key : {"session_id", "request_id", "status", "expires_at", "answer_source", "answer", "selected_index"})
        if (r.contains(key)) reply[key] = r.value(key);
    return reply;
}

void UserQuestion::cancelSession(const QString &sessionId, const QString &status) {
    if (!pendingId.isEmpty() && records.value(pendingId).value("session_id").toString() == sessionId) settle(status);
}

void UserQuestion::cancel(const QString &sessionId, const QString &requestId, const QString &status) {
    if (requestId == pendingId) cancelSession(sessionId, status);
}

void UserQuestion::tick() {
    const auto now = QDateTime::currentSecsSinceEpoch();
    if (!pendingId.isEmpty() && now >= records.value(pendingId).value("expires_at").toInteger())
        settle(QStringLiteral("timed_out"));
    for (auto it = records.begin(); it != records.end();) {
        if (it->value("status").toString() != "pending" && now - it->value("finished_at").toInteger() > 900)
            it = records.erase(it);
        else ++it;
    }
}
