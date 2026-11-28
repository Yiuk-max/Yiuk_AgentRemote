#include "fixtures.h"
#include "gray_image.h"
#include "ncc_matcher.h"

#ifdef YIUK_SPIKE_HAVE_OPENCV
#include <opencv2/core/version.hpp>

#include "opencv_matcher.h"
#endif

#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QGuiApplication>
#include <QImage>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <map>
#include <string>
#include <vector>

namespace {

using vis::GrayPlane;
using vis::Match;
using vis::Rect;
using vis::SearchMode;
using vis::SearchParams;
using vis::SearchResult;

struct SceneEntry {
    std::string name;
    bool positive = true;
    double scale = 1.0;
    bool dark = false;
    GrayPlane gray;
    std::map<std::string, Rect> controls;
};

struct TemplateEntry {
    std::string controlId;
    std::string sourceScene;
    double sourceScale = 1.0;
    bool sourceDark = false;
    GrayPlane gray;
};

struct RouteAgg {
    std::string name;
    std::vector<double> times;
    int total = 0;
    int hits = 0;
    int negEvaluated = 0;
    int negFalsePositives = 0;
    double negMaxScore = -1.0;
    long long nanPatched = 0;
    long long positionsEvaluated = 0;
    double workingSetBeforeMB = 0.0;
    double workingSetPeakMB = 0.0;
    std::map<std::string, std::vector<double>> timesByScene;
    std::map<std::string, int> sceneTotal;
    std::map<std::string, int> sceneHits;
    std::map<std::string, std::vector<double>> timesByBucket;
    std::map<std::string, int> bucketTotal;
    std::map<std::string, int> bucketHits;
    std::map<std::string, double> missScores;  // 失败样本的最高分，便于校准阈值
};

using FindFn = SearchResult (*)(const GrayPlane&, const GrayPlane&, const SearchParams&, SearchMode);

struct LoadedFixture {
    std::vector<SceneEntry> scenes;
    std::vector<TemplateEntry> templates;
    QString manifestPath;
};

bool loadFixture(const QString& manifestPath, LoadedFixture* fixture, QString* error) {
    QFile file(manifestPath);
    if (!file.open(QIODevice::ReadOnly)) {
        *error = QStringLiteral("cannot open manifest %1").arg(manifestPath);
        return false;
    }
    QJsonParseError parseError;
    const QJsonDocument document = QJsonDocument::fromJson(file.readAll(), &parseError);
    if (parseError.error != QJsonParseError::NoError) {
        *error = parseError.errorString();
        return false;
    }
    const QString baseDir = QFileInfo(manifestPath).absolutePath();
    for (const QJsonValue& value : document.object().value(QStringLiteral("scenes")).toArray()) {
        const QJsonObject entry = value.toObject();
        const QString filePath = baseDir + QStringLiteral("/") + entry.value(QStringLiteral("file")).toString();
        const QImage image(filePath);
        if (image.isNull()) {
            *error = QStringLiteral("cannot load scene %1").arg(filePath);
            return false;
        }
        SceneEntry scene;
        scene.name = entry.value(QStringLiteral("name")).toString().toStdString();
        scene.positive = entry.value(QStringLiteral("positive")).toBool(true);
        scene.scale = entry.value(QStringLiteral("scale")).toDouble(1.0);
        scene.dark = entry.value(QStringLiteral("dark")).toBool(false);
        scene.gray = vis::toGray(image);
        for (const QJsonValue& controlValue : entry.value(QStringLiteral("controls")).toArray()) {
            const QJsonObject control = controlValue.toObject();
            const QJsonArray bbox = control.value(QStringLiteral("bbox")).toArray();
            scene.controls[control.value(QStringLiteral("id")).toString().toStdString()] =
                Rect{bbox.at(0).toInt(), bbox.at(1).toInt(), bbox.at(2).toInt(), bbox.at(3).toInt()};
        }
        fixture->scenes.push_back(std::move(scene));
    }
    for (const QJsonValue& value : document.object().value(QStringLiteral("templates")).toArray()) {
        const QJsonObject entry = value.toObject();
        const QString filePath = baseDir + QStringLiteral("/") + entry.value(QStringLiteral("file")).toString();
        const QImage image(filePath);
        if (image.isNull()) {
            *error = QStringLiteral("cannot load template %1").arg(filePath);
            return false;
        }
        TemplateEntry tpl;
        tpl.controlId = entry.value(QStringLiteral("control_id")).toString().toStdString();
        tpl.sourceScene = entry.value(QStringLiteral("source_scene")).toString().toStdString();
        tpl.sourceScale = tpl.sourceScene.find("-100-") != std::string::npos
                              ? 1.0
                              : (tpl.sourceScene.find("-125-") != std::string::npos ? 1.25 : 1.5);
        tpl.sourceDark = tpl.sourceScene.find("dark") != std::string::npos;
        tpl.gray = vis::toGray(image);
        fixture->templates.push_back(std::move(tpl));
    }
    fixture->manifestPath = manifestPath;
    return true;
}

std::string bucketFor(const TemplateEntry& tpl, const SceneEntry& scene) {
    const bool sameScale = std::fabs(tpl.sourceScale - scene.scale) < 0.01;
    const bool sameTheme = tpl.sourceDark == scene.dark;
    return std::string(sameScale ? "same-scale" : "cross-scale") + "/" +
           (sameTheme ? "same-theme" : "cross-theme");
}

int& missDiag(int& budget) { return budget; }
void accumulate(RouteAgg& agg, const std::string& bucket, const std::string& sceneName, bool hit, double elapsedMs,
                const std::string& templateId, double bestScore) {
    agg.total++;
    if (hit) agg.hits++; else agg.missScores[templateId] = std::max(agg.missScores[templateId], bestScore);
    agg.times.push_back(elapsedMs);
    agg.timesByScene[sceneName].push_back(elapsedMs);
    agg.sceneTotal[sceneName]++;
    if (hit) agg.sceneHits[sceneName]++;
    agg.timesByBucket[bucket].push_back(elapsedMs);
    agg.bucketTotal[bucket]++;
    if (hit) agg.bucketHits[bucket]++;
}

void runPositivePass(RouteAgg& agg, FindFn find, SearchMode mode, const LoadedFixture& fixture,
                     const SearchParams& params, int sceneLimit, const std::string& sceneFilter,
                     int dumpMisses, int& missBudget) {
    int used = 0;
    for (const SceneEntry& scene : fixture.scenes) {
        if (!scene.positive) continue;
        if (!sceneFilter.empty() && scene.name != sceneFilter) continue;
        if (sceneLimit > 0 && used >= sceneLimit) break;
        ++used;
        for (const TemplateEntry& tpl : fixture.templates) {
            const auto control = scene.controls.find(tpl.controlId);
            if (control == scene.controls.end()) continue;
            const SearchResult result = find(scene.gray, tpl.gray, params, mode);
            agg.nanPatched += result.nanPatched;
            agg.positionsEvaluated += result.positionsEvaluated;
            const double mem = vis::sampleMemory().workingSetMB;
            agg.workingSetPeakMB = std::max(agg.workingSetPeakMB, mem);

            bool hit = false;
            double bestScore = result.bestScore;
            double predictedCx = -1.0;
            double predictedCy = -1.0;
            if (!result.matches.empty()) {
                const Match& best = result.matches.front();
                bestScore = std::max(bestScore, best.score);
                const double predictedX = best.box.x + best.box.w / 2.0;
                const double predictedY = best.box.y + best.box.h / 2.0;
                const double expectedX = control->second.x + control->second.w / 2.0;
                const double expectedY = control->second.y + control->second.h / 2.0;
                const double distance = std::hypot(predictedX - expectedX, predictedY - expectedY);
                hit = distance <= 6.0 && best.score >= params.threshold;
                predictedCx = predictedX - expectedX;
                predictedCy = predictedY - expectedY;
            }
            // 只打印同主题的未命中：跨主题必然失败，会掩盖真正的问题
            if (!hit && dumpMisses > 0 && tpl.sourceDark == scene.dark && missBudget-- > 0) {
                std::fprintf(stderr, "MISS tpl=%s src=%s scene=%s best=%.3f dx=%.1f dy=%.1f matches=%zu\n",
                             tpl.controlId.c_str(), tpl.sourceScene.c_str(), scene.name.c_str(), bestScore,
                             predictedCx, predictedCy, result.matches.size());
                std::fflush(stderr);
            }
            accumulate(agg, bucketFor(tpl, scene), scene.name, hit, result.elapsedMs, tpl.controlId, bestScore);
        }
    }
}

void runNegativePass(RouteAgg& agg, FindFn find, SearchMode mode, const LoadedFixture& fixture,
                     const SearchParams& params, int sceneLimit, int dumpNegatives,
                     int& negativeBudget) {
    int used = 0;
    for (const SceneEntry& scene : fixture.scenes) {
        if (scene.positive) continue;
        if (sceneLimit > 0 && used >= sceneLimit) break;
        ++used;
        for (const TemplateEntry& tpl : fixture.templates) {
            const SearchResult result = find(scene.gray, tpl.gray, params, mode);
            agg.nanPatched += result.nanPatched;
            agg.negEvaluated++;
            const double best = result.matches.empty() ? result.bestScore : result.matches.front().score;
            agg.negMaxScore = std::max(agg.negMaxScore, best);
            if (!result.matches.empty() && result.matches.front().score >= params.threshold) {
                agg.negFalsePositives++;
                if (dumpNegatives > 0 && negativeBudget-- > 0) {
                    std::fprintf(stderr, "FP tpl=%s src=%s scene=%s score=%.3f center=(%.0f,%.0f)\n",
                                 tpl.controlId.c_str(), tpl.sourceScene.c_str(), scene.name.c_str(),
                                 result.matches.front().score,
                                 result.matches.front().box.x + result.matches.front().box.w / 2.0,
                                 result.matches.front().box.y + result.matches.front().box.h / 2.0);
                    std::fflush(stderr);
                }
            }
        }
    }
}

QJsonObject routeToJson(const RouteAgg& agg, double baselineWorkingSetMB) {
    // 命中数为 0 的键不会被插入 map，取用时必须容忍缺失。
    const auto countOf = [](const std::map<std::string, int>& values, const std::string& key) {
        const auto found = values.find(key);
        return found == values.end() ? 0 : found->second;
    };
    QJsonObject object;
    object.insert(QStringLiteral("samples"), agg.total);
    object.insert(QStringLiteral("hits"), agg.hits);
    object.insert(QStringLiteral("hit_rate"),
                  agg.total > 0 ? double(agg.hits) / double(agg.total) : 0.0);
    object.insert(QStringLiteral("timing_ms"), QString::fromStdString(vis::timingSummary(agg.times)));
    object.insert(QStringLiteral("nan_patched"), double(agg.nanPatched));
    object.insert(QStringLiteral("positions_evaluated"), double(agg.positionsEvaluated));

    QJsonObject negatives;
    negatives.insert(QStringLiteral("samples"), agg.negEvaluated);
    negatives.insert(QStringLiteral("false_positives"), agg.negFalsePositives);
    negatives.insert(QStringLiteral("max_score"), agg.negMaxScore);
    object.insert(QStringLiteral("negatives"), negatives);

    QJsonObject memory;
    memory.insert(QStringLiteral("working_set_start_mb"), baselineWorkingSetMB);
    memory.insert(QStringLiteral("working_set_peak_mb"), agg.workingSetPeakMB);
    memory.insert(QStringLiteral("working_set_peak_delta_mb"), agg.workingSetPeakMB - baselineWorkingSetMB);
    object.insert(QStringLiteral("memory"), memory);

    QJsonArray buckets;
    for (const auto& [bucket, total] : agg.bucketTotal) {
        QJsonObject entry;
        entry.insert(QStringLiteral("bucket"), QString::fromStdString(bucket));
        entry.insert(QStringLiteral("samples"), total);
        entry.insert(QStringLiteral("hits"), countOf(agg.bucketHits, bucket));
        entry.insert(QStringLiteral("hit_rate"), double(countOf(agg.bucketHits, bucket)) / double(total));
        entry.insert(QStringLiteral("timing_ms"),
                     QString::fromStdString(vis::timingSummary(agg.timesByBucket.at(bucket))));
        buckets.append(entry);
    }
    object.insert(QStringLiteral("buckets"), buckets);

    QJsonArray scenes;
    for (const auto& [scene, total] : agg.sceneTotal) {
        QJsonObject entry;
        entry.insert(QStringLiteral("scene"), QString::fromStdString(scene));
        entry.insert(QStringLiteral("samples"), total);
        entry.insert(QStringLiteral("hits"), countOf(agg.sceneHits, scene));
        entry.insert(QStringLiteral("timing_ms"),
                     QString::fromStdString(vis::timingSummary(agg.timesByScene.at(scene))));
        scenes.append(entry);
    }
    object.insert(QStringLiteral("scenes"), scenes);
    return object;
}

std::string summarize(const RouteAgg& agg) {
    char buffer[512];
    std::snprintf(buffer, sizeof(buffer), "samples=%d hit=%.1f%% %s negative_fp=%d/%d max_neg_score=%.3f",
                  agg.total, agg.total > 0 ? 100.0 * double(agg.hits) / double(agg.total) : 0.0,
                  vis::timingSummary(agg.times).c_str(), agg.negFalsePositives, agg.negEvaluated,
                  agg.negMaxScore);
    return buffer;
}

}  // namespace

// P0 基准：同一素材集、同一参数，对比手写 NCC 与 OpenCV 两条路线。
int main(int argc, char** argv) {
    QGuiApplication app(argc, argv);
    QString manifestPath = QStringLiteral("out/fixtures/manifest.json");
    QString outputPath = QStringLiteral("out/p0-bench.json");
    QString ladder = QStringLiteral("default");
    QString route = QStringLiteral("both");
    int positiveLimit = 0;
    int negativeLimit = 0;
    double scaleStep = 1.1;
    QString sceneFilter;
    bool skipNegatives = false;
    int dumpMisses = 0;
    int dumpNegatives = 0;
    bool skipPositives = false;
    int stressIterations = 20;
    int missBudget = 1000000;
    int negativeBudget = 1000000;
    for (int i = 1; i < argc; ++i) {
        const QString arg = QString::fromLocal8Bit(argv[i]);
        if (arg == QLatin1String("--manifest") && i + 1 < argc) {
            manifestPath = QString::fromLocal8Bit(argv[++i]);
        } else if (arg == QLatin1String("--out") && i + 1 < argc) {
            outputPath = QString::fromLocal8Bit(argv[++i]);
        } else if (arg == QLatin1String("--ladder") && i + 1 < argc) {
            ladder = QString::fromLocal8Bit(argv[++i]);
        } else if (arg == QLatin1String("--route") && i + 1 < argc) {
            route = QString::fromLocal8Bit(argv[++i]);
        } else if (arg == QLatin1String("--positive-limit") && i + 1 < argc) {
            positiveLimit = QString::fromLocal8Bit(argv[++i]).toInt();
        } else if (arg == QLatin1String("--negative-limit") && i + 1 < argc) {
            negativeLimit = QString::fromLocal8Bit(argv[++i]).toInt();
        } else if (arg == QLatin1String("--scene") && i + 1 < argc) {
            sceneFilter = QString::fromLocal8Bit(argv[++i]);
        } else if (arg == QLatin1String("--skip-negatives")) {
            skipNegatives = true;
        } else if (arg == QLatin1String("--skip-positives")) {
            skipPositives = true;
        } else if (arg == QLatin1String("--dump-negatives") && i + 1 < argc) {
            dumpNegatives = QString::fromLocal8Bit(argv[++i]).toInt();
        } else if (arg == QLatin1String("--dump-misses") && i + 1 < argc) {
            dumpMisses = QString::fromLocal8Bit(argv[++i]).toInt();
        } else if (arg == QLatin1String("--scale-step") && i + 1 < argc) {
            scaleStep = QString::fromLocal8Bit(argv[++i]).toDouble();
        } else if (arg == QLatin1String("--stress") && i + 1 < argc) {
            stressIterations = std::max(0, QString::fromLocal8Bit(argv[++i]).toInt());
        }
    }

    LoadedFixture fixture;
    QString error;
    if (!loadFixture(manifestPath, &fixture, &error)) {
        std::fprintf(stderr, "load fixture failed: %s\n", qPrintable(error));
        return 1;
    }

    SearchParams params;
    params.threshold = 0.88;
    params.limit = 3;
    params.scaleStep = scaleStep;
    if (ladder == QLatin1String("wide")) {
        params.scaleMin = 0.6;
        params.scaleMax = 1.6;
    }
    const double baseline = vis::sampleMemory().workingSetMB;

    struct RouteResult {
        std::string name;
        RouteAgg agg;
    };
    std::vector<RouteResult> routes;

    const bool runNcc = route != QLatin1String("cv");
    const bool runCv = route != QLatin1String("ncc");
    if (runNcc) {
        RouteAgg ncc;
        ncc.name = "ncc-pyramid";
        ncc.workingSetBeforeMB = vis::sampleMemory().workingSetMB;
        if (!skipPositives) {
            runPositivePass(ncc, &vis::nccFind, SearchMode::Pyramid, fixture, params, positiveLimit,
                            sceneFilter.toStdString(), dumpMisses, missBudget);
        }
        std::printf("[ncc-pyramid] positives %s\n", summarize(ncc).c_str());
        std::fflush(stdout);
        if (!skipNegatives) {
            runNegativePass(ncc, &vis::nccFind, SearchMode::Pyramid, fixture, params, negativeLimit,
                            dumpNegatives, negativeBudget);
        }
        std::printf("[ncc-pyramid] %s\n", summarize(ncc).c_str());
        std::fflush(stdout);
        routes.push_back(RouteResult{ncc.name, ncc});
    }

#ifdef YIUK_SPIKE_HAVE_OPENCV
    if (runCv) {
        RouteAgg cv;
        cv.name = "cv-pyramid";
        cv.workingSetBeforeMB = vis::sampleMemory().workingSetMB;
        if (!skipPositives) {
            runPositivePass(cv, &vis::cvFind, SearchMode::Pyramid, fixture, params, positiveLimit,
                            sceneFilter.toStdString(), dumpMisses, missBudget);
        }
        std::printf("[cv-pyramid] positives %s\n", summarize(cv).c_str());
        std::fflush(stdout);
        if (!skipNegatives) {
            runNegativePass(cv, &vis::cvFind, SearchMode::Pyramid, fixture, params, negativeLimit,
                            dumpNegatives, negativeBudget);
        }
        std::printf("[cv-pyramid] %s\n", summarize(cv).c_str());
        std::fflush(stdout);
        routes.push_back(RouteResult{cv.name, cv});
    }
#endif

    // 精确模式：只取一个模板做一次计时，回答“全量扫描比金字塔慢多少”。
    QJsonObject exactTiming;
    {
        const SceneEntry* scene = nullptr;
        const TemplateEntry* tpl = nullptr;
        for (const SceneEntry& candidate : fixture.scenes) {
            if (candidate.name == "pos-100-light") scene = &candidate;
        }
        for (const TemplateEntry& candidate : fixture.templates) {
            if (candidate.controlId == "top_fav" && candidate.sourceScene == "pos-100-light") tpl = &candidate;
        }
        if (scene && tpl) {
            const SearchResult result = vis::nccFind(scene->gray, tpl->gray, params, SearchMode::Exact);
            exactTiming.insert(QStringLiteral("ncc-exact_ms"), result.elapsedMs);
            exactTiming.insert(QStringLiteral("ncc-exact_score"), result.bestScore);
#ifdef YIUK_SPIKE_HAVE_OPENCV
            const SearchResult cvResult = vis::cvFind(scene->gray, tpl->gray, params, SearchMode::Exact);
            exactTiming.insert(QStringLiteral("cv-exact_ms"), cvResult.elapsedMs);
            exactTiming.insert(QStringLiteral("cv-exact_score"), cvResult.bestScore);
            exactTiming.insert(QStringLiteral("cv-exact_nan"), double(cvResult.nanPatched));
#endif
        }
    }

    // 压力：同一查找重复 N 次，观察耗时与工作集是否持续增长。
    QJsonObject stress;
    {
        const SceneEntry* scene = nullptr;
        const TemplateEntry* tpl = nullptr;
        for (const SceneEntry& candidate : fixture.scenes) {
            if (candidate.name == "pos-100-light") scene = &candidate;
        }
        for (const TemplateEntry& candidate : fixture.templates) {
            if (candidate.controlId == "top_fav" && candidate.sourceScene == "pos-100-light") tpl = &candidate;
        }
        if (scene && tpl && stressIterations > 0) {
            std::vector<double> times;
            const double before = vis::sampleMemory().workingSetMB;
            for (int i = 0; i < stressIterations; ++i) {
                const SearchResult result = vis::nccFind(scene->gray, tpl->gray, params, SearchMode::Pyramid);
                times.push_back(result.elapsedMs);
            }
            const double after = vis::sampleMemory().workingSetMB;
            stress.insert(QStringLiteral("iterations"), stressIterations);
            stress.insert(QStringLiteral("ncc"), QString::fromStdString(vis::timingSummary(times)));
            stress.insert(QStringLiteral("ncc_working_set_delta_mb"), after - before);
        }
    }

    QJsonObject alpha;
#ifdef YIUK_SPIKE_HAVE_OPENCV
    {
        std::string reason;
        bool partialAccepted = false;
        const bool supported = vis::cvMaskedCcoeffFullAndPartial(&reason, &partialAccepted);
        alpha.insert(QStringLiteral("full_mask_matches_unmasked"), supported);
        alpha.insert(QStringLiteral("partial_mask_accepted"), partialAccepted);
        alpha.insert(QStringLiteral("reason"), QString::fromStdString(reason));
    }
#endif

    QJsonObject build;
    build.insert(QStringLiteral("sse2"), true);
#ifdef YIUK_SPIKE_HAVE_OPENCV
    build.insert(QStringLiteral("opencv"), true);
    build.insert(QStringLiteral("opencv_version"),
                 QStringLiteral("%1.%2.%3").arg(CV_VERSION_MAJOR).arg(CV_VERSION_MINOR).arg(CV_VERSION_REVISION));
#else
    build.insert(QStringLiteral("opencv"), false);
#endif

    QJsonObject paramsJson;
    paramsJson.insert(QStringLiteral("ladder"), ladder);
    paramsJson.insert(QStringLiteral("threshold"), params.threshold);
    paramsJson.insert(QStringLiteral("scale_min"), params.scaleMin);
    paramsJson.insert(QStringLiteral("scale_max"), params.scaleMax);
    paramsJson.insert(QStringLiteral("scale_step"), params.scaleStep);
    paramsJson.insert(QStringLiteral("limit"), params.limit);
    paramsJson.insert(QStringLiteral("pyramid_div"), params.pyramidDiv);
    paramsJson.insert(QStringLiteral("route"), route);
    paramsJson.insert(QStringLiteral("positive_limit"), positiveLimit);
    paramsJson.insert(QStringLiteral("negative_limit"), negativeLimit);
    paramsJson.insert(QStringLiteral("scenes"), int(fixture.scenes.size()));
    paramsJson.insert(QStringLiteral("templates"), int(fixture.templates.size()));

    QJsonObject routesJson;
    for (const RouteResult& route : routes) routesJson.insert(QString::fromStdString(route.name), routeToJson(route.agg, route.agg.workingSetBeforeMB));

    QJsonObject root;
    root.insert(QStringLiteral("generated_by"), QStringLiteral("spike/vision vision_bench"));
    root.insert(QStringLiteral("build"), build);
    root.insert(QStringLiteral("params"), paramsJson);
    root.insert(QStringLiteral("routes"), routesJson);
    root.insert(QStringLiteral("exact_single_find"), exactTiming);
    root.insert(QStringLiteral("stress"), stress);
    root.insert(QStringLiteral("alpha"), alpha);

    QDir().mkpath(QFileInfo(outputPath).absolutePath());
    QFile file(outputPath);
    if (file.open(QIODevice::WriteOnly | QIODevice::Truncate)) {
        file.write(QJsonDocument(root).toJson(QJsonDocument::Indented));
        file.close();
        std::printf("results written to %s\n", qPrintable(outputPath));
    } else {
        std::fprintf(stderr, "cannot write %s\n", qPrintable(outputPath));
        return 1;
    }

    std::printf("exact single find: ncc=%.0fms", exactTiming.value(QStringLiteral("ncc-exact_ms")).toDouble());
#ifdef YIUK_SPIKE_HAVE_OPENCV
    std::printf(" cv=%.0fms", exactTiming.value(QStringLiteral("cv-exact_ms")).toDouble());
#endif
    std::printf("\nalpha masked CCOEFF: %s\n",
                alpha.value(QStringLiteral("masked_ccoeff_normed_supported")).toBool() ? "supported" : "rejected");
    return 0;
}
