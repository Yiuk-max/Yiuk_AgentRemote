#include "fixtures.h"

#include <QDir>
#include <QGuiApplication>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>

#include <cstdio>

namespace {

QJsonArray rectToJson(const vis::Rect& rect) { return QJsonArray{rect.x, rect.y, rect.w, rect.h}; }

}  // namespace

// 生成固定素材集：3 种 DPI × 2 种主题的正样本场景、负样本场景，
// 以及从指定场景裁出的模板。输出目录默认 out/fixtures。
int main(int argc, char** argv) {
    QGuiApplication app(argc, argv);
    const QString outputDir = argc > 1 ? QString::fromLocal8Bit(argv[1]) : QStringLiteral("out/fixtures");
    QDir().mkpath(outputDir + QStringLiteral("/scenes"));
    QDir().mkpath(outputDir + QStringLiteral("/templates"));

    const double scales[3] = {1.0, 1.25, 1.5};
    QJsonArray scenesJson;
    for (double scale : scales) {
        for (bool dark : {false, true}) {
            const vis::FixtureScene scene = vis::renderScene(scale, dark, true);
            const QString file = QStringLiteral("scenes/%1.png").arg(QString::fromStdString(scene.name));
            scene.image.save(outputDir + QStringLiteral("/") + file);
            QJsonArray controls;
            for (const vis::FixtureControl& control : scene.controls) {
                QJsonObject entry;
                entry.insert(QStringLiteral("id"), QString::fromStdString(control.id));
                entry.insert(QStringLiteral("bbox"), rectToJson(control.rect));
                controls.append(entry);
            }
            QJsonObject entry;
            entry.insert(QStringLiteral("name"), QString::fromStdString(scene.name));
            entry.insert(QStringLiteral("file"), file);
            entry.insert(QStringLiteral("scale"), scale);
            entry.insert(QStringLiteral("dark"), dark);
            entry.insert(QStringLiteral("positive"), true);
            entry.insert(QStringLiteral("controls"), controls);
            scenesJson.append(entry);
        }
    }
    for (double scale : {1.0, 1.5}) {
        for (bool dark : {false, true}) {
            const vis::FixtureScene scene = vis::renderScene(scale, dark, false);
            const QString file = QStringLiteral("scenes/%1.png").arg(QString::fromStdString(scene.name));
            scene.image.save(outputDir + QStringLiteral("/") + file);
            QJsonArray controls;
            for (const vis::FixtureControl& control : scene.controls) {
                QJsonObject entry;
                entry.insert(QStringLiteral("id"), QString::fromStdString(control.id));
                entry.insert(QStringLiteral("bbox"), rectToJson(control.rect));
                controls.append(entry);
            }
            QJsonObject entry;
            entry.insert(QStringLiteral("name"), QString::fromStdString(scene.name));
            entry.insert(QStringLiteral("file"), file);
            entry.insert(QStringLiteral("scale"), scale);
            entry.insert(QStringLiteral("dark"), dark);
            entry.insert(QStringLiteral("positive"), false);
            entry.insert(QStringLiteral("controls"), controls);
            scenesJson.append(entry);
        }
    }

    // 模板来源：100% 与 150%、浅色与深色。cross-scale / cross-theme 由基准程序组合。
    QJsonArray templatesJson;
    struct Source {
        double scale;
        bool dark;
        const char* scene;
    };
    const Source sources[4] = {{1.0, false, "pos-100-light"}, {1.5, false, "pos-150-light"},
                               {1.0, true, "pos-100-dark"}, {1.5, true, "pos-150-dark"}};
    for (const Source& source : sources) {
        const vis::FixtureScene scene = vis::renderScene(source.scale, source.dark, true);
        for (const vis::FixtureControl& control : scene.controls) {
            const int padding = std::max(2, int(std::lround(3.0 * source.scale)));
            const QImage cropped = vis::cropTemplate(scene.image, control.rect, padding);
            const QString file = QStringLiteral("templates/%1-%2.png")
                                     .arg(QString::fromStdString(control.id))
                                     .arg(QString::fromStdString(scene.name));
            cropped.save(outputDir + QStringLiteral("/") + file);
            QJsonObject entry;
            entry.insert(QStringLiteral("control_id"), QString::fromStdString(control.id));
            entry.insert(QStringLiteral("file"), file);
            entry.insert(QStringLiteral("source_scene"), QString::fromStdString(scene.name));
            entry.insert(QStringLiteral("bbox"), rectToJson(control.rect));
            entry.insert(QStringLiteral("width"), cropped.width());
            entry.insert(QStringLiteral("height"), cropped.height());
            templatesJson.append(entry);
        }
    }

    QJsonObject manifest;
    manifest.insert(QStringLiteral("generated_by"), QStringLiteral("spike/vision make_fixtures"));
    manifest.insert(QStringLiteral("scene_width"), 2048);
    manifest.insert(QStringLiteral("scene_height"), 1280);
    manifest.insert(QStringLiteral("scenes"), scenesJson);
    manifest.insert(QStringLiteral("templates"), templatesJson);
    const QString manifestPath = outputDir + QStringLiteral("/manifest.json");
    QFile file(manifestPath);
    if (!file.open(QIODevice::WriteOnly | QIODevice::Truncate)) {
        std::fprintf(stderr, "cannot write %s\n", qPrintable(manifestPath));
        return 1;
    }
    file.write(QJsonDocument(manifest).toJson(QJsonDocument::Indented));
    file.close();
    std::printf("scenes=%d templates=%d manifest=%s\n", int(scenesJson.size()), int(templatesJson.size()),
                qPrintable(manifestPath));
    return 0;
}
