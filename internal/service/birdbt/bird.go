package birdbt

import (
	"context"
	"github.com/flyqie/memobird-priv-bt/internal/helper/birdproto"
	"github.com/flyqie/memobird-priv-bt/internal/model"
	"github.com/flyqie/memobird-priv-bt/internal/pkg/log"
	"github.com/muka/go-bluetooth/bluez/profile/agent"
	"github.com/muka/go-bluetooth/bluez/profile/device"
	"golang.org/x/sys/unix"
	"os"
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
}

func NewBT(bird *model.Bird, btIface string) *Bird {
	return &Bird{
		bird:    bird,
		btIface: btIface,
	}
}

func (b *Bird) HandleDevice(smartGuid string, dev *device.Device1) {
	tempDevice := &model.BirdDevice{}
	tempDevice.SmartGuid = smartGuid
	tempDevice.Bssid = dev.Properties.Address
	tempDevice.JoinTime = time.Now()
	tempDevice.Elec = -1
	tempDevice.MsgCount = &atomic.Int64{}
	tempDevice.MsgQueueCount = &atomic.Int64{}
	tempDevice.PaperMessageQueue = make(chan *model.BirdPaperMessage, 10000)
	tempDevice.PaperMessageQueueCtx, tempDevice.PaperMessageQueueCancel = context.WithCancel(context.Background())
	defer tempDevice.PaperMessageQueueCancel()
	if os.Getenv("MESSAGE_CB_CHECK") == "ENABLE" {
		tempDevice.PaperMessageCallbackQueue = make(chan string, 10000)
		defer func() {
			defer func() {
				if err := recover(); err != nil {
					btLog.WithError(err.(error)).Errorf("PaperMessageQueueClose")
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
		btLog.WithField("smartGuid", tempDevice.SmartGuid).WithError(err).Errorf("Pair")
		return
	}

	fd, err := unix.Socket(syscall.AF_BLUETOOTH, syscall.SOCK_STREAM, unix.BTPROTO_RFCOMM)
	if err != nil {
		btLog.WithField("smartGuid", tempDevice.SmartGuid).WithError(err).Errorf("Socket")
		return
	}
	err = unix.Connect(fd, &unix.SockaddrRFCOMM{Addr: birdproto.Str2ba(tempDevice.Bssid), Channel: 1})
	if err != nil {
		btLog.WithField("smartGuid", tempDevice.SmartGuid).WithError(err).Errorf("Connect")
		return
	}
	tempDevice.FD = fd
	defer func(tempDevice *model.BirdDevice) {
		_ = unix.Close(tempDevice.FD)
	}(tempDevice)

	// 写入消息
	go b.handlerWriteChan(tempDevice)
	// 读取消息
	buffer := make([]byte, 0)
	bufferMaxLen := 0
	for {
		tmpMsgBytes := make([]byte, 1024)
		n, err := unix.Read(tempDevice.FD, tmpMsgBytes)
		if err != nil {
			btLog.WithField("smartGuid", tempDevice.SmartGuid).WithError(err).Errorf("Read")
			break
		}
		if bufferMaxLen > 0 && len(buffer) < bufferMaxLen {
			// buffer有数据, 说明这次读取的数据是需要拼接的
			// 查看当前数据缺口, 因为可能会有0x00
			needBytes := bufferMaxLen - len(buffer)
			if needBytes > 0 {
				// 缺数据, 需要继续读取
				if n < needBytes {
					// 这个包读完还是不够用, 需要再等包
					buffer = append(buffer, tmpMsgBytes[:n]...)
					continue
				}
				// 这个包够用了
				buffer = append(buffer, tmpMsgBytes[:needBytes]...)
			}
		} else if bufferMaxLen == 0 {
			// buffer没用过, 说明这是全新包
			// 全新包的话判断0xAA第一次出现在哪
			protoMagicI := -1
			for i, v := range tmpMsgBytes {
				if v == 0xAA {
					protoMagicI = i
					break
				}
			}
			// 如果没找到0xAA, 说明这个包是无效包
			if protoMagicI == -1 {
				continue
			}
			// 如果找到了0xAA, 说明这个包是有效包
			tmpMsgBytesValid := make([]byte, 0)
			if protoMagicI != 0 {
				// 如果0xAA不在第一个位置, 说明前面有无效数据
				tmpMsgBytesValid = append(tmpMsgBytesValid, tmpMsgBytes[protoMagicI:]...)
			} else {
				// 这个包开头全是有效的数据!!
				tmpMsgBytesValid = append(tmpMsgBytesValid, tmpMsgBytes...)
			}
			// 获取数据长度
			dataLen := birdproto.ToShort(tmpMsgBytesValid[1:3]) + 4
			if dataLen <= 0 {
				btLog.WithField("smartGuid", tempDevice.SmartGuid).Debugf("ParseChunk size <= 0???")
				continue
			}
			bufferMaxLen = int(dataLen)
			// 如果个包的长度小于需要的数据长度, 说明这个包是不完整的
			if n < bufferMaxLen {
				buffer = append(buffer, tmpMsgBytesValid...)
			} else {
				// 这个包是完整的
				buffer = append(buffer, tmpMsgBytesValid[:bufferMaxLen]...)
			}
		}
		if len(buffer) == bufferMaxLen {
			// 完整读取了!!!
			btLog.WithField("smartGuid", tempDevice.SmartGuid).Debugf("buffer valid")
			handlerMsgBytes := make([]byte, 0)
			handlerMsgBytes = append(handlerMsgBytes, buffer...)
			buffer = make([]byte, 0)
			bufferMaxLen = 0
			go b.handlerMessage(tempDevice, handlerMsgBytes)
		}
		btLog.WithField("smartGuid", tempDevice.SmartGuid).WithField("n", n).Debugf("Received")
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
	_, err := unix.Write(device.FD, birdproto.GetDevice())
	if err != nil {
		btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Errorf("Write GetDevice")
		_ = unix.Close(device.FD)
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
				btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Errorf("makeMsg")
				continue
			}
			device.MsgCount.Add(1)
			_, err = unix.Write(device.FD, msgByte)
			if err != nil {
				btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Errorf("Write")
				break msgHandler
			}
			if device.PaperMessageCallbackQueue != nil && !forceContinue {
				select {
				case cbMsg := <-device.PaperMessageCallbackQueue:
					// 有回调
					btLog.WithField("smartGuid", device.SmartGuid).WithField("cbMsg", cbMsg).Debugf("Paper Callback")
				case <-time.After(10 * time.Minute):
					// 10分钟没收到callback就超时
					btLog.WithField("smartGuid", device.SmartGuid).Debugf("Paper Callback Timeout")
				}
			}
		}
	}
	_ = unix.Close(device.FD)
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
		// 1024byte空字节
		_msgBytes := make([]byte, 1024)
		msgBytes = append(msgBytes, _msgBytes...)
		forceContinue = true
	}
	return msgBytes, forceContinue, nil
}

func (b *Bird) handlerMessage(device *model.BirdDevice, msg []byte) {
	cmdData, cmd, err := birdproto.UnpackData(msg)
	if err != nil {
		btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Errorf("UnpackData")
		return
	}
	if cmd == 1 {
		// 握手指令
		device.Elec = birdproto.CountElec(cmdData[3])
		device.Ver = int64(cmdData[0])
		device.Deep = int(cmdData[1])
		device.Led = int(cmdData[2])
		device.AutoShutdown = int(cmdData[6] & 255)
		device.ErrCode = int(cmdData[5])
	} else if cmd == 3 {
		// 设置指令
		if cmdData[0] != 1 {
			btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Errorf("set failed")
			return
		}
		// 设置指令不会返回新配置项, 所以发个handler去获取
		if _, err = unix.Write(device.FD, birdproto.GetDevice()); err != nil {
			btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Errorf("send get_device failed")
		}
	} else if cmd == 4 {
		// 打印返回
		if cmdData[0] != cmdData[2] || cmdData[1] != cmdData[3] || cmdData[4] != 1 {
			btLog.WithField("smartGuid", device.SmartGuid).WithError(err).Errorf("device return print failed")
		}
		if device.PaperMessageCallbackQueue != nil {
			go func(cbChan chan string, cbMsg string) {
				defer func() {
					if err := recover(); err != nil {
						btLog.WithField("smartGuid", device.SmartGuid).WithError(err.(error)).Errorf("Paper Callback Send")
					}
				}()
				cbChan <- cbMsg
			}(device.PaperMessageCallbackQueue, string(cmdData))
		}
	} else if cmd == 10 {
		// 简略信息上报
		device.Elec = birdproto.CountElec(cmdData[3])
		device.Ver = int64(cmdData[0])
		device.ErrCode = int(cmdData[5])
	} else {
		// 2: 获取状态返回, 咕咕鸡未处理
		// 5: 咕咕鸡固件chunk返回
		// 6: 咕咕鸡icon chunk返回
		// 7/8/9: 未知命令(咕咕鸡也是这么写的, 看起来似乎他们内部也不知道?)
		// 11: 标签纸状态返回
		// 12: 自定义图标指令返回
		// 13: 清除自定义图标返回
		// 14: G5SE接收设置UserId返回
		if cmd == 2 || cmd == 5 || cmd == 6 || cmd == 7 || cmd == 8 || cmd == 9 || cmd == 11 || cmd == 12 || cmd == 13 || cmd == 14 {
			btLog.WithField("smartGuid", device.SmartGuid).WithField("cmd", cmd).Debugf("Unhandle cmd")
		} else {
			btLog.WithField("smartGuid", device.SmartGuid).WithField("cmd", cmd).Errorf("Unknown cmd")
		}
	}
}
