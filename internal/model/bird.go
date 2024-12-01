package model

import (
	"context"
	"github.com/gorilla/websocket"
	"sync"
	"sync/atomic"
	"time"
)

const (
	BIRD_MSG_START = "&*#S#*&"
	BIRD_MSG_END   = "&*#E#*&"
)

type Bird struct {
	// 咕咕机关联map
	DeviceMap     map[string]*BirdDevice
	DeviceMapLock sync.RWMutex
}

type BirdDevice struct {
	// 咕咕鸡设备GUID
	SmartGuid string
	// MAC地址
	Bssid string
	// LED状态
	Led int
	// 蜂鸣器状态
	Buz int
	// 打印速度
	Speed int
	// 固件版本
	Ver int64
	// 语言ID
	LanguageId int
	// 设备忙碌状态
	Busy int
	// 旅程长度信息
	Trip int64
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

	// Websocket连接
	WSConn *websocket.Conn
	// 踢出老设备
	KickOld bool
}

type BirdPaperMessage struct {
	// 重要纸条
	Important bool
	// 纸条内容
	Content []BirdPaperMessageContent
	// 纸条ID
	PaperID int64
	// 纸条总数
	PaperCount int64
	// 纸条序号
	PaperNo int64
}

type BirdPaperMessageContent struct {
	// 消息段类别
	// 文字: 1
	// 图片: 2
	// 图标: 3
	Type int
	// 字体大小
	// FONT_BOLD_FALSE = 0;
	// FONT_BOLD_TRUE = 1;
	// FONT_SIZE1 = 1;
	// FONT_SIZE2 = 2;
	// FONT_UNDERLINE_BOLD = 130;
	// FONT_UNDERLINE_FILAMENT = 129;
	// FONT_UNDERLINE_NULL = 0;
	FontSize int
	// 加粗
	Bold bool
	// 图标ID, 对应图标表
	IconID int64
	// 实际内容(可以是单色位图也可以是文本)
	Content string
}

// S2CPaperMessage sub_1F004D9C
type S2CPaperMessage struct {
	// 固定值为3
	Command int `json:"command"`
	// 纸条内容体
	Content S2CPaperMessageContent `json:"content"`
	// @unused
	IsNotePrint int `json:"isNotePrint"`
	// @unused
	LableHeight int `json:"lableHeight"`
	// 纸条类别, 目前我们只用到了1
	// DraftScrip = 4;
	// SUBSCRIBE = 3;
	// TO_FRIEND = 2;
	// TO_MINE = 1;
	MsgType int `json:"msgType"`
	// 分包总数(打印内容25600字节分包)
	PkgCount int64 `json:"pkgCount"`
	// 分包序号
	PkgNo int64 `json:"pkgNo"`
	// @unknown @used
	PrintCount int64 `json:"printCount"`
	// 咕咕鸡本地实现跟远端下发不一致, 远端是1，本地是(int) System.currentTimeMillis()
	PrintID int64 `json:"printID"`
	// @unused
	PrintSpeed int64 `json:"printSpeed"`
	// 咕咕鸡本地实现跟远端下发不一致???
	PrintTableID int64 `json:"printTableID"`
	// 消息重要级别
	// IMPORTANT = 1;
	// NORMAL = 0;
	Priority int `json:"priority"`
	// @unused
	SmartGUID string `json:"smartGuid"`
	// @unused
	StrDate string `json:"strDate"`
	// @unused
	StudyToUserID string `json:"studyToUserId"`
	// @unused
	StudyUserID string `json:"studyUserId"`
	// @unused
	ToUserID int64 `json:"toUserId"`
	// @unused
	UserID int64 `json:"userId"`
}

type S2CPaperMessageContent struct {
	// @unused
	PaperLogoState int `json:"paperLogoState"`
	// 消息内容体
	TextList []S2CPaperMessageContentTextList `json:"textList"`
}

type S2CPaperMessageContentTextList struct {
	// 单一内容结构文本
	// if (i != 1) {
	//            if (i == 3) {
	//                str = CommonAPI.base64EncodeBuffer(str.getBytes());
	//            }
	//            this.basetext = str;
	//            return;
	//        }
	//        try {
	//            this.basetext = CommonAPI.base64EncodeBuffer((str + "\n").getBytes("GBK"));
	//        } catch (UnsupportedEncodingException e) {
	//            e.printStackTrace();
	//        }
	// 常态是base64编码的GBK文本
	Basetext string `json:"basetext"`
	// 加粗
	Bold int `json:"bold"`
	// @unused
	EncodeType int64 `json:"encodeType"`
	// 字体大小
	// FONT_BOLD_FALSE = 0;
	// FONT_BOLD_TRUE = 1;
	// FONT_SIZE1 = 1;
	// FONT_SIZE2 = 2;
	// FONT_UNDERLINE_BOLD = 130;
	// FONT_UNDERLINE_FILAMENT = 129;
	// FONT_UNDERLINE_NULL = 0;
	FontSize int `json:"fontSize"`
	// 图标ID, 对应图标表
	IconID int64 `json:"iconID"`
	// @unused
	IsHead bool `json:"isHead"`
	// @unused
	IsLongPicture bool `json:"isLongPicture"`
	// @unused
	IsSignature bool `json:"isSignature"`
	// @unused
	OriginalImgPath string `json:"originalImgPath"`
	// 消息段类别
	// ICON = 4;
	// IMAGE = 5;
	// QRCODE = 3;
	// TEXT = 1;
	PrintType int `json:"printType"`
	// @unused
	Underline int `json:"underline"`
}

type C2SAuth struct {
	Command   int    `json:"command"`
	SmartGuid string `json:"smartGuid"`
	Bssid     string `json:"bssid"`
	Led       int    `json:"led"`
	Buz       int    `json:"buz"`
	Speed     int    `json:"speed"`
	Ver       int64  `json:"ver"`
}

type C2SReportDeviceInfo struct {
	Command int `json:"command"`
	MsgType int `json:"msgType"`
	Result  int `json:"result"`

	// command: 2
	Led        int `json:"led"`
	Buz        int `json:"buz"`
	Speed      int `json:"speed"`
	LanguageId int `json:"languageId"`

	// command: 3
	PrintTableID int64 `json:"printTableID"`
	PackageCount int64 `json:"packageCount"`
	PackageNo    int64 `json:"packageNo"`

	// command: 4
	Trip int64 `json:"trip"`

	// command: 88
	Busy int `json:"busy"`
}
