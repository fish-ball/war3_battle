//go:build windows

// w3bridge-gui 是魔兽争霸3互联网联机工具的图形界面客户端。
// 默认连接 war3.aiunit.com:51234、房间 666，玩家名自动填入当前 Windows 用户名。
package main

import (
	"context"
	"log"
	"os"
	"strconv"

	"github.com/lxn/walk"

	"war3_battle/internal/client"
	"war3_battle/internal/w3gs"
)

// 默认参数
const (
	defaultServer    = "war3.aiunit.com:51234"
	defaultRoom      = "666"
	defaultVersion   = "27"
	defaultGamePort  = "6112"
	defaultProxyBase = "26112"
)

// stringModel 下拉框的静态字符串列表模型
type stringModel struct {
	walk.ListModelBase
	items []string
}

func (m *stringModel) ItemCount() int {
	return len(m.items)
}

func (m *stringModel) Value(index int) interface{} {
	return m.items[index]
}

// app 主界面与运行控制
type app struct {
	mw *walk.MainWindow

	editServer    *walk.LineEdit
	editRoom      *walk.LineEdit
	editPass      *walk.LineEdit
	editName      *walk.LineEdit
	editVersion   *walk.LineEdit
	comboProduct  *walk.ComboBox
	editGamePort  *walk.LineEdit
	editProxyBase *walk.LineEdit

	btnStart *walk.PushButton
	btnStop  *walk.PushButton

	logView *LogView

	cancel  context.CancelFunc
	running bool
}

func main() {
	log.SetFlags(log.Ltime)

	a := &app{}
	if err := a.build(); err != nil {
		log.Fatalf("[界面] 创建窗口失败: %v", err)
	}
	log.SetOutput(a.logView)
	log.Print("[界面] 魔兽争霸3互联网联机工具已就绪，点“启动”连接房间")

	a.mw.Run()

	// 窗口关闭后取消后台任务，等待其自行清理
	if a.cancel != nil {
		a.cancel()
	}
}

// build 创建主窗口与全部控件
func (a *app) build() error {
	mw, err := walk.NewMainWindow()
	if err != nil {
		return err
	}
	a.mw = mw

	if err := mw.SetTitle("魔兽争霸3互联网联机工具"); err != nil {
		return err
	}
	if err := mw.SetSize(walk.Size{Width: 620, Height: 560}); err != nil {
		return err
	}
	if err := mw.SetMinMaxSize(walk.Size{Width: 520, Height: 440}, walk.Size{}); err != nil {
		return err
	}
	if err := mw.SetLayout(walk.NewVBoxLayout()); err != nil {
		return err
	}

	// 连接设置区：两列网格（标签 | 输入框）
	gb, err := walk.NewGroupBox(mw)
	if err != nil {
		return err
	}
	if err := gb.SetTitle("连接设置"); err != nil {
		return err
	}
	grid := walk.NewGridLayout()
	grid.SetColumnStretchFactor(1, 1)
	if err := gb.SetLayout(grid); err != nil {
		return err
	}

	if err := a.buildFields(gb, grid); err != nil {
		return err
	}

	// 日志区
	a.logView, err = NewLogView(mw)
	if err != nil {
		return err
	}

	// 按钮区
	bar, err := walk.NewComposite(mw)
	if err != nil {
		return err
	}
	if err := bar.SetLayout(walk.NewHBoxLayout()); err != nil {
		return err
	}
	a.btnStart, err = walk.NewPushButton(bar)
	if err != nil {
		return err
	}
	if err := a.btnStart.SetText("启动"); err != nil {
		return err
	}
	a.btnStart.Clicked().Attach(a.onStart)

	a.btnStop, err = walk.NewPushButton(bar)
	if err != nil {
		return err
	}
	if err := a.btnStop.SetText("停止"); err != nil {
		return err
	}
	a.btnStop.SetEnabled(false)
	a.btnStop.Clicked().Attach(a.onStop)

	// 显示窗口（Run 不会自动显示）
	mw.Show()
	return nil
}

// buildFields 创建表单输入控件并填入默认值
func (a *app) buildFields(parent walk.Container, grid *walk.GridLayout) error {
	var err error

	if a.editServer, err = walk.NewLineEdit(parent); err != nil {
		return err
	}
	a.editServer.SetText(defaultServer)

	if a.editRoom, err = walk.NewLineEdit(parent); err != nil {
		return err
	}
	a.editRoom.SetText(defaultRoom)

	if a.editPass, err = walk.NewLineEdit(parent); err != nil {
		return err
	}

	if a.editName, err = walk.NewLineEdit(parent); err != nil {
		return err
	}
	// 玩家名默认取当前 Windows 用户名
	a.editName.SetText(os.Getenv("USERNAME"))

	if a.editVersion, err = walk.NewLineEdit(parent); err != nil {
		return err
	}
	a.editVersion.SetText(defaultVersion)

	if a.comboProduct, err = walk.NewComboBox(parent); err != nil {
		return err
	}
	if err := a.comboProduct.SetModel(&stringModel{items: []string{
		"冰封王座 (TFT)",
		"混乱之治 (ROC)",
	}}); err != nil {
		return err
	}
	a.comboProduct.SetCurrentIndex(0)

	if a.editGamePort, err = walk.NewLineEdit(parent); err != nil {
		return err
	}
	a.editGamePort.SetText(defaultGamePort)

	if a.editProxyBase, err = walk.NewLineEdit(parent); err != nil {
		return err
	}
	a.editProxyBase.SetText(defaultProxyBase)

	rows := []struct {
		label string
		input walk.Widget
	}{
		{"服务端地址", a.editServer},
		{"房间名", a.editRoom},
		{"房间密码（可空）", a.editPass},
		{"玩家名", a.editName},
		{"游戏版本（1.27 填 27）", a.editVersion},
		{"游戏类型", a.comboProduct},
		{"游戏端口", a.editGamePort},
		{"代理端口起始值", a.editProxyBase},
	}
	for i, row := range rows {
		label, err := walk.NewLabel(parent)
		if err != nil {
			return err
		}
		if err := label.SetText(row.label); err != nil {
			return err
		}
		if err := grid.SetRange(label, walk.Rectangle{X: 0, Y: i, Width: 1, Height: 1}); err != nil {
			return err
		}
		if err := grid.SetRange(row.input, walk.Rectangle{X: 1, Y: i, Width: 1, Height: 1}); err != nil {
			return err
		}
	}
	return nil
}

// onStart 连接服务端并启动联机客户端
func (a *app) onStart() {
	if a.running {
		return
	}

	server := a.editServer.Text()
	room := a.editRoom.Text()
	name := a.editName.Text()
	if server == "" || room == "" || name == "" {
		log.Print("[界面] 服务端地址、房间名、玩家名不能为空")
		return
	}

	version, err := strconv.ParseUint(a.editVersion.Text(), 10, 32)
	if err != nil {
		log.Printf("[界面] 游戏版本格式不正确: %q", a.editVersion.Text())
		return
	}
	gamePort, err := strconv.ParseUint(a.editGamePort.Text(), 10, 16)
	if err != nil {
		log.Printf("[界面] 游戏端口格式不正确: %q", a.editGamePort.Text())
		return
	}
	proxyBase, err := strconv.Atoi(a.editProxyBase.Text())
	if err != nil {
		log.Printf("[界面] 代理端口起始值格式不正确: %q", a.editProxyBase.Text())
		return
	}

	product := w3gs.ProductTFT
	if a.comboProduct.CurrentIndex() == 1 {
		product = w3gs.ProductROC
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.setRunning(true)
	log.Printf("[界面] 正在连接房间 %s（服务端 %s）...", room, server)

	go func() {
		c := client.New(client.Config{
			ServerAddr: server,
			Room:       room,
			Password:   a.editPass.Text(),
			Name:       name,
			Product:    product,
			Version:    uint32(version),
			GamePort:   uint16(gamePort),
			ProxyBase:  proxyBase,
		})
		err := c.Run(ctx)
		a.mw.Synchronize(func() {
			a.setRunning(false)
			if err != nil {
				log.Printf("[界面] 已停止: %v", err)
			} else {
				log.Print("[界面] 已停止")
			}
		})
	}()
}

// onStop 断开连接并停止客户端
func (a *app) onStop() {
	if !a.running || a.cancel == nil {
		return
	}
	log.Print("[界面] 正在断开...")
	a.cancel()
}

// setRunning 切换运行状态下的控件可用性
func (a *app) setRunning(running bool) {
	a.running = running
	a.editServer.SetEnabled(!running)
	a.editRoom.SetEnabled(!running)
	a.editPass.SetEnabled(!running)
	a.editName.SetEnabled(!running)
	a.editVersion.SetEnabled(!running)
	a.comboProduct.SetEnabled(!running)
	a.editGamePort.SetEnabled(!running)
	a.editProxyBase.SetEnabled(!running)
	a.btnStart.SetEnabled(!running)
	a.btnStop.SetEnabled(running)
}
