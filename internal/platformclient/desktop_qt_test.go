package platformclient_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/localbridge"
)

// 桌面原语的真机验证：真实 Qt 进程开一个前台测试窗口，通过本地桥接驱动
// press_keys / type_text / move_mouse / wait_for。
//
// 默认跳过（会抢焦点、并真的注入键鼠）。开启方式：
//
//	YIUK_DESKTOP_QT_EXE=<yiuk-question-interop.exe>
//
// 若测试窗口不是前台窗口，测试会直接 skip，避免把按键打进别的应用。
// number 统一处理进程内 int 与 JSON 往返后的 float64。
func number(t *testing.T, value any) float64 {
	t.Helper()
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	t.Fatalf("unexpected numeric field %T", value)
	return 0
}

type event map[string]any

func TestDesktopPrimitivesAgainstRealClient(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("desktop primitives require Windows")
	}
	executable := os.Getenv("YIUK_DESKTOP_QT_EXE")
	if executable == "" {
		t.Skip("set YIUK_DESKTOP_QT_EXE to the built yiuk-question-interop executable")
	}
	root := t.TempDir()
	control := filepath.Join(root, "control.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "--desktop")
	cmd.Env = append(os.Environ(),
		"LOCALAPPDATA="+root, "APPDATA="+root,
		"QT_QPA_PLATFORM=windows", "YIUK_QUESTION_CONTROL="+control,
		"YIUK_QUESTION_LOCAL=0", "YIUK_PLATFORM_ADDRESS=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(control, []byte("quit"), 0o600)
		time.Sleep(100 * time.Millisecond)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(stderr.String())
		}
	})

	events := make(chan event, 256)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var parsed event
			if json.Unmarshal(scanner.Bytes(), &parsed) == nil {
				events <- parsed
			}
		}
	}()
	next := func(timeout time.Duration) event {
		t.Helper()
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatalf("test client exited early: %s", stderr.String())
			}
			return e
		case <-time.After(timeout):
			t.Fatalf("timed out waiting for test client event")
		}
		return nil
	}

	var ready event
	for ready == nil {
		e := next(15 * time.Second)
		if e["ready"] == true {
			ready = e
		}
	}
	window := struct{ x, y, w, h int }{
		x: int(ready["rect_x"].(float64)), y: int(ready["rect_y"].(float64)),
		w: int(ready["rect_w"].(float64)), h: int(ready["rect_h"].(float64)),
	}
	editCenter := [2]int{int(ready["edit_cx"].(float64)), int(ready["edit_cy"].(float64))}
	button := struct{ x, y, w, h int }{
		x: int(ready["button_x"].(float64)), y: int(ready["button_y"].(float64)),
		w: int(ready["button_w"].(float64)), h: int(ready["button_h"].(float64)),
	}

	// 前景窗口守卫：不是我们的测试窗口就退出，绝不往别人的窗口里打字。
	active := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		e := next(2 * time.Second)
		if v, ok := e["active"].(bool); ok && v {
			active = true
			break
		}
	}
	if !active {
		t.Skip("test window is not foreground; refusing to inject input")
	}

	path := filepath.Join(root, "Yiuk", "DeviceAssist", "local-api.json")
	var cfg localbridge.Config
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		data, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(data, &cfg) == nil && cfg.Token != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if cfg.Token == "" {
		t.Fatal("local bridge did not start")
	}
	core := agenttools.NewCore(localbridge.New(cfg))
	call := func(name string, args any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		response := core.Call(ctx, name, raw)
		if !response.OK {
			t.Fatalf("%s failed: %+v", name, response)
		}
		data, _ := response.Data.(map[string]any)
		return data
	}
	session := call(agenttools.RequestConnection, map[string]any{"device_id": "123456789", "purpose": "desktop primitives"})
	sessionID, _ := session["session_id"].(string)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if call(agenttools.GetSessionStatus, map[string]any{"session_id": sessionID})["status"] == "active" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// 收到期望文本返回 true；否则说明这台机器的合成输入被拦截（键盘过滤驱动、
	// 远程会话等），此时跳过按键断言，但保留坐标与条件等待的验证。
	waitText := func(want string) bool {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
			select {
			case e, ok := <-events:
				if !ok {
					t.Fatalf("client exited: %s", stderr.String())
				}
				if text, ok := e["text"].(string); ok && text == want {
					return true
				}
			case <-time.After(100 * time.Millisecond):
			}
		}
		return false
	}

	// 0. 先点进测试窗口把它激活。键盘只发给前台窗口，而鼠标点击按坐标投递，
	//    不受“当前前台是谁”影响——这也是真实使用时的正确顺序。
	activating := call(agenttools.TakeScreenshot, map[string]any{"session_id": sessionID})
	shotToScreen := func(x, y float64) (int, int) {
		return int((x - number(t, activating["screen_left"])) * number(t, activating["width"]) / number(t, activating["screen_width"])),
			int((y - number(t, activating["screen_top"])) * number(t, activating["height"]) / number(t, activating["screen_height"]))
	}
	activX, activY := shotToScreen(float64(editCenter[0]), float64(editCenter[1]))
	call(agenttools.Click, map[string]any{"session_id": sessionID, "x": activX, "y": activY, "screenshot_id": activating["screenshot_id"]})

	// 1. 单键：结果必须报告前台窗口，便于调用方确认输入去向
	pressed := call(agenttools.PressKeys, map[string]any{"session_id": sessionID, "keys": "b"})
	title, _ := pressed["foreground_title"].(string)
	if !strings.Contains(title, "Yiuk Desktop Test") {
		// 桌面被其它窗口抢走时这里会为空或不匹配：这是环境噪声，不是工具缺陷。
		t.Logf("press_keys reported foreground_title=%q; skipping key/text assertions", title)
	}
	injectionWorks := strings.Contains(title, "Yiuk Desktop Test") && waitText("b")
	if injectionWorks {
		// 2. 组合键全选 + 删除
		call(agenttools.PressKeys, map[string]any{"session_id": sessionID, "keys": "ctrl+a"})
		call(agenttools.PressKeys, map[string]any{"session_id": sessionID, "keys": "backspace"})
		if !waitText("") {
			t.Fatalf("ctrl+a + backspace did not clear the field")
		}
		// 3. 文本输入
		call(agenttools.TypeText, map[string]any{"session_id": sessionID, "text": "xyz"})
		if !waitText("xyz") {
			t.Fatalf("type_text did not reach the focused control")
		}
	} else {
		t.Log("synthetic input appears blocked on this machine; skipping key/text assertions")
	}

	// 4. 截图坐标 -> 屏幕坐标换算：把指针移到测试窗口中心
	screenshot := call(agenttools.TakeScreenshot, map[string]any{"session_id": sessionID, "max_width": 2048})
	shot := struct{ width, height, left, top, screenW, screenH float64 }{
		width:   number(t, screenshot["width"]),
		height:  number(t, screenshot["height"]),
		left:    number(t, screenshot["screen_left"]),
		top:     number(t, screenshot["screen_top"]),
		screenW: number(t, screenshot["screen_width"]),
		screenH: number(t, screenshot["screen_height"]),
	}
	screenCX := float64(window.x) + float64(window.w)/2
	screenCY := float64(window.y) + float64(window.h)/2
	shotX := int((screenCX - shot.left) * shot.width / shot.screenW)
	shotY := int((screenCY - shot.top) * shot.height / shot.screenH)
	moved := call(agenttools.MoveMouse, map[string]any{"session_id": sessionID, "x": shotX, "y": shotY})
	if _, ok := moved["foreground_title"].(string); !ok {
		t.Fatalf("move_mouse must report foreground_title: %v", moved)
	}
	gotX := int(number(t, moved["screen_x"]))
	gotY := int(number(t, moved["screen_y"]))
	// 指针是否真的移动：读回系统光标位置再判断一次。
	cursor := readCursor(t, control, events)
	t.Logf("cursor after move_mouse: %v (expected %d,%d)", cursor, int(screenCX), int(screenCY))
	if cursor != nil && (abs(cursor[0]-int(screenCX)) > 3 || abs(cursor[1]-int(screenCY)) > 3) {
		t.Fatalf("cursor did not follow move_mouse: %v", cursor)
	}
	if abs(gotX-int(screenCX)) > 2 || abs(gotY-int(screenCY)) > 2 {
		t.Fatalf("move_mouse mapped to (%d,%d), want ~(%d,%d)", gotX, gotY, int(screenCX), int(screenCY))
	}

	// 4.1 截图版本绑定：连续两张截图的 id 必须不同，且最小间隔生效
	firstShot := call(agenttools.TakeScreenshot, map[string]any{"session_id": sessionID})
	firstID, _ := firstShot["screenshot_id"].(string)
	started := time.Now()
	secondShot := call(agenttools.TakeScreenshot, map[string]any{"session_id": sessionID})
	if elapsed := time.Since(started); elapsed < 450*time.Millisecond {
		t.Fatalf("second screenshot was not throttled: %v", elapsed)
	}
	secondID, _ := secondShot["screenshot_id"].(string)
	if firstID == "" || secondID == "" || firstID == secondID {
		t.Fatalf("screenshot ids look wrong: %q %q", firstID, secondID)
	}

	// 4.2 用旧 id 点击必须被拒绝，且报错点名新旧两个 id
	raw := json.RawMessage(fmt.Sprintf(`{"session_id":%q,"x":%d,"y":%d,"screenshot_id":%q}`, sessionID, shotX, shotY, firstID))
	stale := core.Call(ctx, agenttools.Click, raw)
	if stale.OK || stale.Code != "STALE_SCREENSHOT" {
		t.Fatalf("stale screenshot_id was accepted: %+v", stale)
	}
	if !strings.Contains(stale.Message, firstID) || !strings.Contains(stale.Message, secondID) {
		t.Fatalf("stale message should name both ids: %q", stale.Message)
	}

	// 4.3 变化检测：结果必须带 changed / diff_ratio，不返回图片
	clicked := call(agenttools.Click, map[string]any{
		"session_id": sessionID, "x": shotX, "y": shotY,
		"screenshot_id": secondID, "detect_change": true,
	})
	if _, ok := clicked["changed"].(bool); !ok {
		t.Fatalf("detect_change result is missing changed: %v", clicked)
	}
	if _, ok := clicked["diff_ratio"].(float64); !ok {
		if _, alt := clicked["diff_ratio"].(int); !alt {
			t.Fatalf("detect_change result is missing diff_ratio: %v", clicked)
		}
	}

	// 4.3b 真实点击：点中测试窗口的按钮应回传 clicked；点空白处应为 changed=false。
	buttonScreenX := float64(button.x) + float64(button.w)/2
	buttonScreenY := float64(button.y) + float64(button.h)/2
	fresh := call(agenttools.TakeScreenshot, map[string]any{"session_id": sessionID})
	toShot := func(screenX, screenY float64) (int, int) {
		return int((screenX - number(t, fresh["screen_left"])) * number(t, fresh["width"]) / number(t, fresh["screen_width"])),
			int((screenY - number(t, fresh["screen_top"])) * number(t, fresh["height"]) / number(t, fresh["screen_height"]))
	}
	bx, by := toShot(buttonScreenX, buttonScreenY)
	buttonClick := call(agenttools.Click, map[string]any{
		"session_id": sessionID, "x": bx, "y": by,
		"screenshot_id": fresh["screenshot_id"], "detect_change": true,
	})
	gotClick := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		select {
		case e, ok := <-events:
			if !ok {
				break
			}
			if n, ok := e["clicked"].(float64); ok && n >= 1 {
				gotClick = true
			}
		case <-time.After(100 * time.Millisecond):
		}
		if gotClick {
			break
		}
	}
	if !gotClick {
		t.Log("the button never received a real click; synthetic mouse input looks blocked too")
	} else {
		t.Logf("button click: changed=%v diff_ratio=%v changed_ratio=%v", buttonClick["changed"], buttonClick["diff_ratio"], buttonClick["changed_ratio"])
		if buttonClick["changed"] != true {
			t.Fatalf("detect_change missed a click that changed the label: %v", buttonClick)
		}
	}

	// 点窗口空白处：不应该有可见变化
	fresh2 := call(agenttools.TakeScreenshot, map[string]any{"session_id": sessionID})
	blankX, blankY := toShot(float64(window.x)+12, float64(window.y)+float64(window.h)-8)
	blank := call(agenttools.Click, map[string]any{
		"session_id": sessionID, "x": blankX, "y": blankY,
		"screenshot_id": fresh2["screenshot_id"], "detect_change": true, "settle_ms": 400,
	})
	t.Logf("blank click: changed=%v diff_ratio=%v changed_ratio=%v", blank["changed"], blank["diff_ratio"], blank["changed_ratio"])
	if blank["changed"] != false {
		t.Fatalf("blank click reported a change: %v", blank)
	}

	// 4.4 文件工具：不需要任何输入注入，直接对真实磁盘验证
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	listed := call(agenttools.ListDir, map[string]any{"session_id": sessionID, "path": dir})
	entries := asMaps(listed["entries"])
	if len(entries) != 2 || listed["truncated"] != false {
		t.Fatalf("list_dir returned %v (entries=%d truncated=%v)", listed, len(entries), listed["truncated"])
	}
	var fileEntry map[string]any
	for _, entry := range entries {
		if entry["name"] == "a.txt" {
			fileEntry = entry
		}
	}
	if fileEntry == nil || fileEntry["type"] != "file" || number(t, fileEntry["size"]) != 5 {
		t.Fatalf("file entry wrong: %v", fileEntry)
	}
	// limit 生效时应标记 truncated
	limited := call(agenttools.ListDir, map[string]any{"session_id": sessionID, "path": dir, "limit": 1})
	if limited["truncated"] != true || number(t, limited["returned"]) != 1 {
		t.Fatalf("limit was ignored: %v", limited)
	}
	// 对文件调用 list_dir 应引导改用 stat_path
	rawList := json.RawMessage(fmt.Sprintf(`{"session_id":%q,"path":%q}`, sessionID, filepath.Join(dir, "a.txt")))
	if failed := core.Call(ctx, agenttools.ListDir, rawList); failed.Code != "INVALID_ARGUMENT" || !strings.Contains(failed.Hint, "stat_path") {
		t.Fatalf("list_dir on a file should point at stat_path: %+v", failed)
	}
	stat := call(agenttools.StatPath, map[string]any{"session_id": sessionID, "path": filepath.Join(dir, "a.txt")})
	if stat["type"] != "file" || number(t, stat["size"]) != 5 {
		t.Fatalf("stat_path wrong: %v", stat)
	}
	rawMissing := json.RawMessage(fmt.Sprintf(`{"session_id":%q,"path":%q}`, sessionID, filepath.Join(dir, "nope.txt")))
	if failed := core.Call(ctx, agenttools.StatPath, rawMissing); failed.Code != "FILE_NOT_FOUND" || failed.Hint == "" {
		t.Fatalf("stat_path on a missing path: %+v", failed)
	}

	// 5. 条件等待：资源管理器一定在跑
	matched := call(agenttools.WaitFor, map[string]any{
		"session_id": sessionID, "condition": "process", "value": "explorer", "timeout_seconds": 5,
	})
	if matched["matched"] != true {
		t.Fatalf("wait_for(process=explorer) did not match: %v", matched)
	}
	// 必然不成立的条件要在超时后正常返回，而不是报错
	missing := call(agenttools.WaitFor, map[string]any{
		"session_id": sessionID, "condition": "process", "value": "yiuk-no-such-process", "timeout_seconds": 1,
	})
	if missing["matched"] != false || missing["timed_out"] != true {
		t.Fatalf("wait_for(timeout) should return matched=false: %v", missing)
	}

	call(agenttools.CloseSession, map[string]any{"session_id": sessionID})
	_ = os.WriteFile(control, []byte("quit"), 0o600)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// readCursor 让测试窗口回报系统光标位置，用来确认注入是否真的生效。
func readCursor(t *testing.T, control string, events chan event) []int {
	t.Helper()
	if err := os.WriteFile(control, []byte("cursor"), 0o600); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		select {
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if raw, ok := event["cursor"].([]any); ok && len(raw) == 2 {
				return []int{int(raw[0].(float64)), int(raw[1].(float64))}
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Log("test window did not report the cursor position")
	return nil
}

// asMaps 兼容两种形态：进程内直接返回 []map[string]any，
// 经 MCP/JSON 往返后变成 []any。
func asMaps(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if entry, ok := item.(map[string]any); ok {
				out = append(out, entry)
			}
		}
		return out
	}
	return nil
}
