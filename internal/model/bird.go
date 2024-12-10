package model

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
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

type BirdDevice struct {
	// 咕咕鸡设备GUID
	SmartGuid string
	// MAC地址
	Bssid string
	// LED状态
	Led int
	// 打印浓度
	// 5: 浅
	// 7: 适中
	// 9: 深
	Deep int
	// 固件版本
	Ver int64
	// 自动关机
	AutoShutdown int
	// 电量
	Elec int
	// 设备错误代码
	ErrCode int
	// 设备加入时间
	JoinTime time.Time
	// 已发送消息数量
	MsgCount *atomic.Int64
	// 队列消息数量
	MsgQueueCount *atomic.Int64
	// 设备型号
	Model string

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
