#pragma once

#include <QImage>

#include <string>
#include <vector>

#include "vision_common.h"

namespace vis {

struct FixtureControl {
    std::string id;
    Rect rect;  // 场景像素坐标
};

struct FixtureScene {
    std::string name;
    double scale = 1.0;
    bool dark = false;
    bool positive = true;
    QImage image;
    std::vector<FixtureControl> controls;
};

// 渲染一张固定素材场景。
// scale 同时改变控件物理像素尺寸与文字渲染尺寸，模拟 100% / 125% / 150% DPI 下的真实桌面。
// positive=false 时绘制“相似但不同”的负样本（描边 / 变体图标 + 不同文本）。
FixtureScene renderScene(double scale, bool dark, bool positive);

QImage cropTemplate(const QImage& scene, const Rect& rect, int padding);

const std::vector<std::string>& positiveControlIds();

}  // namespace vis
