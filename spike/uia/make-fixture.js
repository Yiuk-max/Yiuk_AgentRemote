// Generates spike/uia/fixture.html: a local page with known control names and
// a deliberately deep/large DOM, so the Edge (Chromium/UIA) measurements are
// reproducible without network access and without touching a real browsing
// session.
const fs = require('fs');
const path = require('path');

const rows = Array.from({ length: 120 }, (_, i) =>
  '      <tr><td>文件 ' + (i + 1) + '.txt</td><td>' + ((i * 37) % 5000) + ' KB</td><td>2024-03-' + String((i % 28) + 1).padStart(2, '0') + '</td></tr>'
).join('\n');

let inner = '';
for (let depth = 0; depth < 14; depth++) {
  inner = '<div class="level" data-level="' + depth + '">' + inner + '</div>';
}

const html = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>YiukSpikeFixture</title>
<style>
  body { font-family: "Segoe UI", sans-serif; margin: 0; }
  .bar { display: flex; gap: 8px; padding: 8px; background: #222; color: #eee; }
  .bar button { padding: 6px 12px; }
  .tabs { display: flex; gap: 4px; padding: 6px; background: #333; }
  .tab[aria-selected="true"] { background: #4a6; }
  .tab { padding: 6px 14px; background: #444; color: #fff; }
  main { padding: 12px; }
  .level { border-left: 2px solid #ddd; padding-left: 6px; }
  table { border-collapse: collapse; }
  th, td { border: 1px solid #ccc; padding: 3px 8px; font-size: 12px; }
</style>
</head>
<body>
<div class="bar">
  <button id="back">后退</button>
  <button id="forward">前进</button>
  <button id="refresh">刷新</button>
  <input id="omnibox" aria-label="地址和搜索栏" value="file:///fixture" size="50">
  <button id="star">将此页面添加到收藏夹</button>
  <button id="favorites">收藏夹</button>
  <button id="more">设置及其他</button>
</div>
<div class="tabs" role="tablist" aria-label="标签页栏">
  <div class="tab" role="tab" aria-selected="true">标签一</div>
  <div class="tab" role="tab">标签二</div>
  <div class="tab" role="tab">标签三</div>
</div>
<main>
  <h1>UIA spike fixture</h1>
  <p>已知名字的控件：地址和搜索栏（输入框）、收藏夹（按钮）、设置及其他（按钮）、标签页栏（tablist）、在页面中搜索（输入框）。</p>
  <p><input id="search" type="search" aria-label="在页面中搜索" placeholder="在页面中搜索"></p>
  ${inner}
  <table>
    <thead><tr><th>名称</th><th>大小</th><th>修改日期</th></tr></thead>
    <tbody>
${rows}
    </tbody>
  </table>
</main>
<script>
  // Feedback that a UIA Invoke really ran: the window title changes, which the
  // probe can observe without reading the page through the accessibility tree.
  document.querySelectorAll("button").forEach(function (button) {
    button.addEventListener("click", function () {
      document.title = "YiukSpikeFixture clicked:" + button.textContent.trim();
      var label = document.createElement("p");
      label.textContent = "clicked " + button.textContent.trim();
      document.body.appendChild(label);
    });
  });
</script>
</body>
</html>
`;

const out = path.join(__dirname, 'fixture.html');
fs.writeFileSync(out, html, 'utf8');
console.log(out + ' ' + html.length + ' bytes');
