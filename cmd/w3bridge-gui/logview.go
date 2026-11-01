//go:build windows

// 本文件的 LogView 控件改写自 walk 官方示例 examples/logview（BSD 许可证）。
package main

import (
	"errors"
	"strings"
	"syscall"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"
)

// logAppendMsg 自定义消息：通知控件从队列取一段文本追加
const logAppendMsg = win.WM_USER + 6

// LogView 只读多行日志控件，支持任意线程写入
type LogView struct {
	walk.WidgetBase
	logChan chan string
}

// NewLogView 创建日志控件
func NewLogView(parent walk.Container) (*LogView, error) {
	lv := &LogView{logChan: make(chan string, 1024)}
	if err := walk.InitWidget(
		lv,
		parent,
		"EDIT",
		win.WS_TABSTOP|win.WS_VISIBLE|win.WS_VSCROLL|win.ES_MULTILINE|win.ES_WANTRETURN,
		win.WS_EX_CLIENTEDGE); err != nil {
		return nil, err
	}
	if err := lv.setReadOnly(true); err != nil {
		return nil, err
	}
	// 去掉编辑控件默认的文本长度上限
	lv.SendMessage(win.EM_SETLIMITTEXT, 4294967295, 0)
	return lv, nil
}

// CreateLayoutItem 让布局把控件按可拉伸项处理，占据剩余空间
func (*LogView) CreateLayoutItem(ctx *walk.LayoutContext) walk.LayoutItem {
	return walk.NewGreedyLayoutItem()
}

// Write 实现 io.Writer 接口，可直接作为 log 包的输出目标（线程安全）
func (lv *LogView) Write(p []byte) (int, error) {
	lv.PostAppendText(strings.TrimRight(string(p), "\r\n") + "\r\n")
	return len(p), nil
}

// PostAppendText 线程安全地追加文本
func (lv *LogView) PostAppendText(value string) {
	lv.logChan <- value
	win.PostMessage(lv.Handle(), logAppendMsg, 0, 0)
}

// AppendText 在 UI 线程把文本追加到末尾
func (lv *LogView) AppendText(value string) {
	length := int(lv.SendMessage(win.WM_GETTEXTLENGTH, 0, 0))
	lv.SendMessage(win.EM_SETSEL, uintptr(length), uintptr(length))
	lv.SendMessage(win.EM_REPLACESEL, 0, uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(value))))
}

// setReadOnly 设置只读
func (lv *LogView) setReadOnly(readOnly bool) error {
	if 0 == lv.SendMessage(win.EM_SETREADONLY, uintptr(win.BoolToBOOL(readOnly)), 0) {
		return errors.New("设置日志控件只读失败")
	}
	return nil
}

// WndProc 处理追加文本的自定义消息
func (lv *LogView) WndProc(hwnd win.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case win.WM_GETDLGCODE:
		if wParam == win.VK_RETURN {
			return win.DLGC_WANTALLKEYS
		}
		return win.DLGC_HASSETSEL | win.DLGC_WANTARROWS | win.DLGC_WANTCHARS
	case logAppendMsg:
		select {
		case value := <-lv.logChan:
			lv.AppendText(value)
		default:
			return 0
		}
	}

	return lv.WidgetBase.WndProc(hwnd, msg, wParam, lParam)
}
