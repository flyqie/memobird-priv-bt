package model

import (
	"context"
	"errors"
	"golang.org/x/sys/unix"
	"sync"
	"sync/atomic"
	"time"
)

// 设备连接相关的错误
var (
	// ErrDeviceClosed 设备连接已关闭
	ErrDeviceClosed = errors.New("device connection closed")
	// ErrDeviceShortWrite 底层写入了 0 字节
	ErrDeviceShortWrite = errors.New("device short write")
)

type Bird struct {
	// 咕咕机关联map
	DeviceMap     map[string]*BirdDevice
	DeviceMapLock sync.RWMutex

	// 咕咕机临时连接map
	DeviceTempMap     map[string]time.Time
	DeviceTempMapLock sync.RWMutex
	// 蓝牙管理器chan, 负责蓝牙设备的发现
	BtMgrChan chan string
}

// BirdDeviceState 是设备状态快照。
//
// 这些字段由收包协程（birdbt）写入、由 RPC 协程（mgr）读取，
// 必须走 Snapshot() / UpdateState()，不要直接读写 BirdDevice 上的字段。
type BirdDeviceState struct {
	// LED状态
	Led int
	// 打印浓度(1-30)
	Deep int
	// 固件版本原始值，显示时除以 10
	Ver int64
	// 自动关机(分钟)，0 = 不自动关机
	AutoShutdown int
	// 电量百分比，-1 表示还没读到
	Elec int
	// 设备状态 / 错误码，含义见 birdproto.DeviceState
	ErrCode int
}

type BirdDevice struct {
	// 咕咕鸡设备GUID
	SmartGuid string
	// MAC地址
	Bssid string
	// 设备型号（蓝牙设备名）。连接时确定，之后不再变，可以直接读。
	Model string
	// 设备加入时间
	JoinTime time.Time
	// 已发送消息数量
	MsgCount *atomic.Int64
	// 队列消息数量
	MsgQueueCount *atomic.Int64

	// 消息队列
	PaperMessageQueue chan *BirdPaperMessage
	// 消息队列ctx
	PaperMessageQueueCtx context.Context
	// 消息队列cancel
	PaperMessageQueueCancel context.CancelFunc
	// 消息队列分包并发锁
	PaperMessageQueuePkgLock sync.RWMutex
	// 消息回调队列
	PaperMessageCallbackQueue chan string

	// RFCOMM连接FD
	FD int

	// writeLock 串行化对 FD 的写入。
	// RFCOMM 是字节流，并发 unix.Write 会把两个帧的字节交错，设备端校验和必挂。
	// 所有出站数据都要走 Write()。
	writeLock sync.Mutex
	// fdClosed 标记 FD 是否已关闭，保证 Close() 幂等
	fdClosed bool

	// stateLock 保护 state
	stateLock sync.RWMutex
	state     BirdDeviceState
	// notifyLock 保护 stateNotify
	notifyLock  sync.Mutex
	stateNotify chan struct{}

	// connDone 在连接协程退出时被关闭。
	// 由 InitConn 在设备被放进 DeviceMap **之前**创建，之后只读，所以不用加锁。
	connDone     chan struct{}
	connDoneOnce sync.Once
}

// InitConn 初始化连接生命周期信号，必须在设备被放进 DeviceMap 之前调用。
func (d *BirdDevice) InitConn() {
	d.connDone = make(chan struct{})
}

// MarkConnDone 标记连接协程已退出（幂等，由连接协程 defer 调用）。
func (d *BirdDevice) MarkConnDone() {
	d.connDoneOnce.Do(func() {
		if d.connDone != nil {
			close(d.connDone)
		}
	})
}

// WaitConnDone 等连接协程退出，超时返回 false。
// KickDevice 关掉 fd 后要等收包协程把自己从 DeviceMap 摘掉，
// 否则紧接着的 DeviceDetail 还会看到这台设备。
func (d *BirdDevice) WaitConnDone(timeout time.Duration) bool {
	if d.connDone == nil {
		return true
	}
	select {
	case <-d.connDone:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Snapshot 返回当前设备状态快照。
func (d *BirdDevice) Snapshot() BirdDeviceState {
	d.stateLock.RLock()
	defer d.stateLock.RUnlock()
	return d.state
}

// UpdateState 就地更新设备状态，并唤醒正在等状态的协程。
func (d *BirdDevice) UpdateState(fn func(*BirdDeviceState)) {
	d.stateLock.Lock()
	fn(&d.state)
	d.stateLock.Unlock()

	d.notifyLock.Lock()
	if d.stateNotify != nil {
		close(d.stateNotify)
		d.stateNotify = nil
	}
	d.notifyLock.Unlock()
}

// stateUpdated 返回一个 channel，设备下一次上报状态时被关闭。
// 必须在发出请求**之前**调用，否则应答可能比等待先到。
func (d *BirdDevice) stateUpdated() <-chan struct{} {
	d.notifyLock.Lock()
	defer d.notifyLock.Unlock()
	if d.stateNotify == nil {
		d.stateNotify = make(chan struct{})
	}
	return d.stateNotify
}

// RefreshState 先挂上等待、再执行 send（通常是发一帧握手），然后等设备上报一次状态。
// 顺序不能反：先 send 再挂等待的话，设备回得快就会错过通知。
// 超时或 send 失败返回 false，调用方退回缓存值即可，这里不报错。
func (d *BirdDevice) RefreshState(timeout time.Duration, send func() error) bool {
	updated := d.stateUpdated()
	if err := send(); err != nil {
		return false
	}
	select {
	case <-updated:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Write 串行化地向设备写入数据，并处理短写。
func (d *BirdDevice) Write(p []byte) error {
	d.writeLock.Lock()
	defer d.writeLock.Unlock()
	if d.fdClosed || d.FD <= 0 {
		return ErrDeviceClosed
	}
	for len(p) > 0 {
		n, err := unix.Write(d.FD, p)
		if err != nil {
			return err
		}
		if n <= 0 {
			return ErrDeviceShortWrite
		}
		p = p[n:]
	}
	return nil
}

// Close 幂等关闭设备连接。
func (d *BirdDevice) Close() error {
	d.writeLock.Lock()
	defer d.writeLock.Unlock()
	if d.fdClosed || d.FD <= 0 {
		return nil
	}
	d.fdClosed = true
	return unix.Close(d.FD)
}

// IsOpen 报告连接是否仍然可用。
func (d *BirdDevice) IsOpen() bool {
	d.writeLock.Lock()
	defer d.writeLock.Unlock()
	return !d.fdClosed && d.FD > 0
}

type BirdPaperMessage struct {
	// 纸条内容
	Content BirdPaperMessageContent
	// 纸条ID
	PaperID int64
	// 纸条总数
	PaperCount int64
	// 纸条序号
	PaperNo int64
}

type BirdPaperMessageContent struct {
	// 消息段类别
	// 1024byte空字节: 0
	// 文字: 1
	// 图片: 2
	// 图标: 3
	// QR: 4
	Type int
	// 下划线
	Underline bool
	// 加粗
	Bold bool
	// 长图
	LongPic bool
	// 图标ID, 对应图标表
	IconID int64
	// 实际内容(可以是单色位图也可以是文本)
	Content string
}
