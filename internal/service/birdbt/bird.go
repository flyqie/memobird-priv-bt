package birdbt

import (
	"bytes"
	"context"
	"errors"
	"github.com/flyqie/memobird-priv-bt/internal/helper/birdproto"
	"github.com/flyqie/memobird-priv-bt/internal/model"
	"github.com/flyqie/memobird-priv-bt/internal/pkg/log"
	"github.com/muka/go-bluetooth/bluez/profile/agent"
	"github.com/muka/go-bluetooth/bluez/profile/device"
	"golang.org/x/sys/unix"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

var btLog log.Logger

func init() {
	btLog = log.Get("bt")
}

type Bird struct {
	bird    *model.Bird
	btIface string
	// packetInterval 每发一包后的间隔，0 = 不限速
	packetInterval time.Duration
}

func NewBT(bird *model.Bird, btIface string) *Bird {
	return &Bird{
		bird:           bird,
		btIface:        btIface,
		packetInterval: packetIntervalFromEnv(),
	}
}

// packetIntervalFromEnv 读取 BT_PACKET_INTERVAL_MS，默认 0（不限速）。
// 官方 SDK 每包 sleep 50ms，但没真机确定不了该不该限，所以做成开关：
// 出现丢包就设 BT_PACKET_INTERVAL_MS=50 试试。
func packetIntervalFromEnv() time.Duration {
	raw := os.Getenv("BT_PACKET_INTERVAL_MS")
	if raw == "" {
		return 0
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms < 0 {
		btLog.WithField("value", raw).Warn("invalid BT_PACKET_INTERVAL_MS, falling back to 0")
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// deviceName 取蓝牙设备名。
// Name 是远端广播的原始名字，Alias 是本地别名，只在 Name 为空时退回 Alias。
// 机型判定（电量曲线）依赖这个名字，取错电量就会算错。
func deviceName(dev *device.Device1) string {
	if dev == nil || dev.Properties == nil {
		return ""
	}
	if dev.Properties.Name != "" {
		return dev.Properties.Name
	}
	return dev.Properties.Alias
}

func (b *Bird) HandleDevice(smartGuid string, dev *device.Device1) {
	tempDevice := &model.BirdDevice{}
	tempDevice.SmartGuid = smartGuid
	tempDevice.Bssid = dev.Properties.Address
	tempDevice.Model = deviceName(dev)
	tempDevice.JoinTime = time.Now()
	// 电量 -1 表示还没读到；状态字段要走 UpdateState
	tempDevice.UpdateState(func(s *model.BirdDeviceState) { s.Elec = -1 })
	tempDevice.MsgCount = &atomic.Int64{}
	tempDevice.MsgQueueCount = &atomic.Int64{}
	tempDevice.PaperMessageQueue = make(chan *model.BirdPaperMessage, 10000)
	tempDevice.PaperMessageQueueCtx, tempDevice.PaperMessageQueueCancel = context.WithCancel(context.Background())
	tempDevice.InitConn()
	// 注册在最前面 → defer 后进先出，它最后执行，保证 KickDevice 的
	// WaitConnDone 返回时设备已从 DeviceMap 摘掉、fd 也关了
	defer tempDevice.MarkConnDone()
	defer tempDevice.PaperMessageQueueCancel()
	if os.Getenv("MESSAGE_CB_CHECK") == "ENABLE" {
		tempDevice.PaperMessageCallbackQueue = make(chan string, 10000)
		defer func() {
			defer func() {
				if err := recover(); err != nil {
					btLog.WithError(err.(error)).Error("close paper callback queue failed")
				}
			}()
			close(tempDevice.PaperMessageCallbackQueue)
		}()
	} else {
		tempDevice.PaperMessageCallbackQueue = nil
	}
	b.bird.DeviceMapLock.Lock()
	b.bird.DeviceMap[tempDevice.SmartGuid] = tempDevice
	b.bird.DeviceMapLock.Unlock()

	// 主协程退出之后同时干掉MAP里的设备
	defer func(tempDevice *model.BirdDevice) {
		b.bird.DeviceMapLock.Lock()
		delete(b.bird.DeviceMap, tempDevice.SmartGuid)
		b.bird.DeviceMapLock.Unlock()
	}(tempDevice)
	err := b.btPair(dev, b.btIface)
	if err != nil {
		btLog.WithField("smartGuid", tempDevice.SmartGuid).WithError(err).Error("pair device failed")
		return
	}

	rfcommAddr, err := birdproto.ParseMAC(tempDevice.Bssid)
	if err != nil {
		btLog.WithField("smartGuid", tempDevice.SmartGuid).WithField("bssid", tempDevice.Bssid).WithError(err).Error("parse MAC address failed")
		return
	}
	fd, err := unix.Socket(syscall.AF_BLUETOOTH, syscall.SOCK_STREAM, unix.BTPROTO_RFCOMM)
	if err != nil {
		btLog.WithField("smartGuid", tempDevice.SmartGuid).WithError(err).Error("open RFCOMM socket failed")
		return
	}
	err = unix.Connect(fd, &unix.SockaddrRFCOMM{Addr: rfcommAddr, Channel: 1})
	if err != nil {
		btLog.WithField("smartGuid", tempDevice.SmartGuid).WithError(err).Error("connect RFCOMM failed")
		return
	}
	tempDevice.FD = fd
	defer func() {
		_ = tempDevice.Close()
	}()

	// 写入消息
	go b.handlerWriteChan(tempDevice)
	// 读取消息
	// 串口是字节流，一次 Read 可能返回半个帧，也可能返回两个半帧，
	// 所以这里用累积缓冲区做「找帧头 → 按 L 取整帧 → 循环」的处理。
	buffer := make([]byte, 0, 4096)
	readBuf := make([]byte, 4096)
	for {
		n, err := unix.Read(tempDevice.FD, readBuf)
		if err != nil {
			btLog.WithField("smartGuid", tempDevice.SmartGuid).WithError(err).Error("read from device failed")
			break
		}
		if n <= 0 {
			continue
		}
		buffer = append(buffer, readBuf[:n]...)

		// 一次 Read 里可能含有多个完整帧，全部取出来
		for {
			// 丢弃帧头之前的无效字节
			magicI := bytes.IndexByte(buffer, birdproto.FrameMagic)
			if magicI < 0 {
				buffer = buffer[:0]
				break
			}
			if magicI > 0 {
				btLog.WithField("smartGuid", tempDevice.SmartGuid).WithField("drop", magicI).Debug("drop bytes before frame magic")
				buffer = append(buffer[:0], buffer[magicI:]...)
			}
			frameLen, err := birdproto.FrameLength(buffer)
			if err != nil {
				if errors.Is(err, birdproto.ErrFrameTooShort) {
					// 不足 3 字节算不出帧长，等下一次读取
					break
				}
				// 长度字段非法：这个 0xAA 不是真正的帧头，丢掉后重新对齐
				btLog.WithField("smartGuid", tempDevice.SmartGuid).WithError(err).Debug("drop invalid frame length")
				buffer = append(buffer[:0], buffer[1:]...)
				continue
			}
			if len(buffer) < frameLen {
				break
			}
			frame := make([]byte, frameLen)
			copy(frame, buffer[:frameLen])
			buffer = append(buffer[:0], buffer[frameLen:]...)
			btLog.WithField("smartGuid", tempDevice.SmartGuid).WithField("frameLen", frameLen).Debug("frame received")
			go b.handlerMessage(tempDevice, frame)
		}
	}
}

func (b *Bird) btPair(dev *device.Device1, adapterID string) error {
	props, err := dev.GetProperties()
	if err != nil {
		return err
	}
	if props.Connected {
		return nil
	}

	if !props.Paired || !props.Trusted {
		err = dev.Pair()
		if err != nil {
			return err
		}

		err = agent.SetTrusted(adapterID, dev.Path())
		if err != nil {
			return err
		}
	}

	/*
		if !props.Connected {
			err = dev.Connect()
			if err != nil {
				return err
			}
		}
	*/

	return nil
}

func (b *Bird) handlerWriteChan(device *model.BirdDevice) {
	time.Sleep(2 * time.Second)
	if err := device.Write(birdproto.GetDevice()); err != nil {
		btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Error("send handshake failed")
		_ = device.Close()
		return
	}

msgHandler:
	for {
		select {
		case <-device.PaperMessageQueueCtx.Done():
			// 退出
			break msgHandler
		case msg := <-device.PaperMessageQueue:
			// 有消息了
			device.MsgQueueCount.Add(-1)
			msgByte, forceContinue, err := b.makeMsg(device, msg)
			if err != nil {
				btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Error("build message failed")
				continue
			}
			device.MsgCount.Add(1)
			if err = device.Write(msgByte); err != nil {
				btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Error("write to device failed")
				break msgHandler
			}
			// 官方 SDK 每包 sleep 50ms，这里默认不限速，见 packetIntervalFromEnv
			if b.packetInterval > 0 {
				time.Sleep(b.packetInterval)
			}
			if device.PaperMessageCallbackQueue != nil && !forceContinue {
				select {
				case cbMsg := <-device.PaperMessageCallbackQueue:
					// 有回调
					btLog.WithField("smartGuid", device.SmartGuid).WithField("cbMsg", cbMsg).Debug("paper callback received")
				case <-time.After(10 * time.Minute):
					// 10分钟没收到callback就超时
					btLog.WithField("smartGuid", device.SmartGuid).Debug("paper callback timed out")
				}
			}
		}
	}
	_ = device.Close()
}

func (b *Bird) makeMsg(device *model.BirdDevice, msg *model.BirdPaperMessage) ([]byte, bool, error) {
	var forceContinue bool
	msgBytes := make([]byte, 0)
	if msg.Content.Type == 1 {
		// 打印文字
		_msgBytes := birdproto.PrintText([]byte(msg.Content.Content), int(msg.PaperNo), int(msg.PaperCount), msg.Content.Bold, msg.Content.Underline)
		msgBytes = append(msgBytes, _msgBytes...)
	} else if msg.Content.Type == 2 {
		// 打印图片
		_msgBytes := birdproto.PrintIMG([]byte(msg.Content.Content), msg.Content.LongPic, int(msg.PaperNo), int(msg.PaperCount))
		msgBytes = append(msgBytes, _msgBytes...)
	} else if msg.Content.Type == 3 {
		// 打印icon
		_msgBytes := birdproto.PrintIcon(int(msg.Content.IconID), int(msg.PaperNo), int(msg.PaperCount))
		msgBytes = append(msgBytes, _msgBytes...)
	} else if msg.Content.Type == 4 {
		// 打印QR
		_msgBytes := birdproto.PrintQR([]byte(msg.Content.Content), int(msg.PaperNo), int(msg.PaperCount))
		msgBytes = append(msgBytes, _msgBytes...)
	} else if msg.Content.Type == 0 {
		// 打印前导空包（唤醒设备 / 清空缓冲），不带协议帧
		msgBytes = append(msgBytes, make([]byte, birdproto.WakeupPacketLen)...)
		forceContinue = true
	}
	return msgBytes, forceContinue, nil
}

func (b *Bird) handlerMessage(device *model.BirdDevice, raw []byte) {
	frame, err := birdproto.UnpackFrame(raw)
	if err != nil {
		btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Error("parse frame failed")
		return
	}

	switch frame.Cmd {
	case birdproto.CmdHandshake, birdproto.CmdPrinterState:
		// CMD 1: 应答「获取设备信息」
		// CMD 10: 设备主动上报，字段是子集，必须按 TAG 是否存在逐个更新
		b.applyDeviceState(device, frame.Params)

	case birdproto.CmdSetting:
		if !birdproto.ParseSettingResult(frame.Params) {
			btLog.WithField("smartGuid", device.SmartGuid).Error("device rejected setting")
			return
		}
		// 设置应答不回传新配置，主动再握一次手把状态刷新回来
		if err := device.Write(birdproto.GetDevice()); err != nil {
			btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Error("send handshake failed")
		}

	case birdproto.CmdPrint:
		res := birdproto.ParsePrintResult(frame.Params)
		if !res.OK() {
			btLog.WithField("smartGuid", device.SmartGuid).
				WithField("pkgCount", res.TotalPackets()).
				WithField("pkgNo", res.PacketNo()).
				WithField("flag", res.Flag).
				Errorf("device return print failed")
		}
		if device.PaperMessageCallbackQueue != nil {
			cbMsg := string(frame.Params)
			go func(cbChan chan string, cbMsg string) {
				defer func() {
					if err := recover(); err != nil {
						btLog.WithField("smartGuid", device.SmartGuid).WithField("panic", err).Error("deliver paper callback failed")
					}
				}()
				cbChan <- cbMsg
			}(device.PaperMessageCallbackQueue, cbMsg)
		}

	case birdproto.CmdGetStatus, birdproto.CmdOTA, birdproto.CmdIcon,
		birdproto.CmdGetLabelState, birdproto.CmdCustomIcon, birdproto.CmdClearIcon,
		birdproto.CmdSetUserID:
		// 2/5/6/11/12/13/14 的应答，本服务不处理（12/13/14/5/6 是破坏性操作，永不实现）
		btLog.WithField("smartGuid", device.SmartGuid).WithField("cmd", frame.Cmd.String()).Debug("command not handled")

	default:
		btLog.WithField("smartGuid", device.SmartGuid).WithField("cmd", int(frame.Cmd)).Error("unknown command")
	}
}

// applyDeviceState 用握手 / 状态上报的 PARAMS 更新设备缓存。
// 只更新本次应答中真实出现的字段，避免 CMD 10 的子集上报把已知配置清零。
func (b *Bird) applyDeviceState(device *model.BirdDevice, params []byte) {
	hs := birdproto.ParseHandshake(params)
	// 机型来自蓝牙设备名，只影响电量曲线，连接后不变
	elecCurve := birdproto.DetectModelKind(device.Model)
	device.UpdateState(func(s *model.BirdDeviceState) {
		if hs.Has(birdproto.ParamVersion) {
			s.Ver = int64(hs.Version)
		}
		if hs.Has(birdproto.ParamDeep) {
			s.Deep = hs.Deep
		}
		if hs.Has(birdproto.ParamLed) {
			s.Led = hs.Led
		}
		if hs.Has(birdproto.ParamAutoShutdown) {
			s.AutoShutdown = hs.AutoShutdown
		}
		if hs.Has(birdproto.ParamState) {
			s.ErrCode = int(hs.State)
		}
		if hs.Has(birdproto.ParamBattery) {
			s.Elec = birdproto.CountElecForModel(byte(hs.Battery), elecCurve)
		}
	})
	snap := device.Snapshot()
	btLog.WithField("smartGuid", device.SmartGuid).
		WithField("ver", snap.Ver).
		WithField("deep", snap.Deep).
		WithField("led", snap.Led).
		WithField("elec", snap.Elec).
		WithField("state", hs.State.String()).
		Debugf("DeviceState")
}
