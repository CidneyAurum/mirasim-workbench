//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
)

// windowOptions 描述桌面窗口的初始形态。
type windowOptions struct {
	URL      string
	Title    string
	IconPath string
	DataDir  string
	Width    uint
	Height   uint
	Debug    bool
	Done     <-chan struct{}
}

var (
	user32DLL        = syscall.NewLazyDLL("user32.dll")
	procSendMessageW = user32DLL.NewProc("SendMessageW")
	procLoadImageW   = user32DLL.NewProc("LoadImageW")
	procSetWindowPos = user32DLL.NewProc("SetWindowPos")
	procDwmSetAttr   = syscall.NewLazyDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
)

// init 在任何窗口创建之前声明「每显示器 DPI 感知 v2」。
//
// 避免 Windows 在高 DPI 显示器上拉伸整个窗口位图；WebView2 按设备缩放渲染。
func init() { enableDPIAwareness() }

func enableDPIAwareness() {
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	if p := user32DLL.NewProc("SetProcessDpiAwarenessContext"); p.Find() == nil {
		if r, _, _ := p.Call(^uintptr(3)); r != 0 {
			return
		}
	}
	if p := syscall.NewLazyDLL("shcore.dll").NewProc("SetProcessDpiAwareness"); p.Find() == nil {
		// PROCESS_PER_MONITOR_DPI_AWARE = 2（Win8.1 起）
		if r, _, _ := p.Call(2); r == 0 {
			return
		}
	}
	// 老系统兜底：系统级 DPI 感知
	_, _, _ = user32DLL.NewProc("SetProcessDPIAware").Call()
}

// systemDPI 返回主显示器 DPI（96 = 100%）。
func systemDPI() uintptr {
	if p := user32DLL.NewProc("GetDpiForSystem"); p.Find() == nil {
		if dpi, _, _ := p.Call(); dpi > 0 {
			return dpi
		}
	}
	if p := syscall.NewLazyDLL("gdi32.dll").NewProc("GetDeviceCaps"); p.Find() == nil {
		if hdc, _, _ := user32DLL.NewProc("GetDC").Call(0); hdc != 0 {
			dpi, _, _ := p.Call(hdc, 88) // LOGPIXELSX
			if dpi > 0 {
				return dpi
			}
		}
	}
	return 96
}

const (
	wmSetIcon      = 0x0080
	iconSmall      = 0
	iconBig        = 1
	imageIcon      = 1
	lrLoadFromFile = 0x00000010
)

// runAppWindow 打开 WebView2 桌面窗口承载工作台界面；窗口关闭后返回。
//
// 与「开浏览器标签页」的区别在这里：它是独立的 Win32 顶层窗口（自己的任务栏按钮、
// 图标、标题），没有地址栏/标签页/前进后退，用的是本仓库目录下的独立 WebView2
// 数据目录（data/webview2），不碰用户 Edge 的个人配置。
func runAppWindow(opt windowOptions) error {
	// 逻辑尺寸（按 96 DPI 设计）换算成物理像素，保证 125%/150% 缩放下观感一致。
	dpi := systemDPI()
	scale := func(v uint) uint {
		if dpi <= 96 {
			return v
		}
		return v * uint(dpi) / 96
	}
	width, height := scale(opt.Width), scale(opt.Height)

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     opt.Debug,
		AutoFocus: true,
		DataPath:  opt.DataDir,
		WindowOptions: webview2.WindowOptions{
			Title:  opt.Title,
			Width:  width,
			Height: height,
			Center: true,
		},
	})
	if w == nil {
		return fmt.Errorf("无法创建 WebView2 窗口（缺少 WebView2 运行时？）")
	}
	defer w.Destroy()
	closed := make(chan struct{})
	defer close(closed)
	if opt.Done != nil {
		go func() {
			select {
			case <-opt.Done:
				w.Dispatch(func() { w.Terminate() })
			case <-closed:
			}
		}()
	}

	// 最小尺寸：再小布局（左侧栏 + 卡片网格）会挤坏。
	w.SetSize(int(width), int(height), webview2.HintMin)
	hwnd := uintptr(w.Window())
	setDarkTitleBar(hwnd)
	if opt.IconPath != "" {
		setWindowIcon(hwnd, opt.IconPath)
	}
	w.Navigate(opt.URL)
	w.Run()
	return nil
}

// setDarkTitleBar 把系统标题栏切成暗色。
//
// 界面本身是纯暗色的，标题栏却跟着系统主题走亮色的话，顶上会横着一条白带，
// 非常割裂。DWMWA_USE_IMMERSIVE_DARK_MODE 在 20H1 之后是 20，更早的 1809 用 19，
// 两个都试一遍，成功与否都不影响主界面。
func setDarkTitleBar(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	on := int32(1)
	size := unsafe.Sizeof(on)
	for _, attr := range []uintptr{20, 19} {
		if r, _, _ := procDwmSetAttr.Call(hwnd, attr, uintptr(unsafe.Pointer(&on)), size); r == 0 {
			break
		}
	}
	// 边框重绘一次，否则要等窗口下一次改变大小才会换色。
	const (
		swpNoSize       = 0x0001
		swpNoMove       = 0x0002
		swpNoActivate   = 0x0010
		swpFrameChanged = 0x0020
	)
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoSize|swpNoMove|swpNoActivate|swpFrameChanged)
}

// setWindowIcon 把 .ico 装到窗口上（标题栏左上角 + 任务栏）。
//
// 库只在 IconId 指向资源时用 exe 内嵌图标，我们这里是外部 .ico 文件，所以直接
// LoadImageW 从磁盘取，再分别按 ICON_BIG / ICON_SMALL 两个槽位发 WM_SETICON；
// 只发一个槽位的话任务栏和 Alt-Tab 里会有一处是默认图标。
func setWindowIcon(hwnd uintptr, path string) {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	for _, slot := range []struct {
		which uintptr
		size  uintptr
	}{
		{iconBig, 32},
		{iconSmall, 16},
	} {
		h, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(ptr)), imageIcon, slot.size, slot.size, lrLoadFromFile)
		if h == 0 {
			continue
		}
		procSendMessageW.Call(hwnd, wmSetIcon, slot.which, h)
	}
}
