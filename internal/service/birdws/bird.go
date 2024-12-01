package birdws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/flyqie/memobird-priv/internal/model"
	"github.com/flyqie/memobird-priv/internal/pkg/log"
	"github.com/gorilla/websocket"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var wsLog log.Logger

func init() {
	wsLog = log.Get("ws")
}

type Bird struct {
	bird     *model.Bird
	upgrader websocket.Upgrader
}

func NewWS(bird *model.Bird) *Bird {
	return &Bird{
		bird: bird,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// 允许所有来源的连接
				return true
			},
		},
	}
}

func (b *Bird) Bind(mux *http.ServeMux) {
	mux.HandleFunc("/", b.handleWebSocket)
}

func (b *Bird) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// 升级 HTTP 连接为 WebSocket
	// 修复咕咕机sec-websocket-key不合法问题
	r.Header.Set("Sec-WebSocket-Key", "5pZ1QbQDw77H8Jpw8UEF1w==")
	conn, err := b.upgrader.Upgrade(w, r, nil)
	if err != nil {
		wsLog.WithError(err).Errorf("Failed to upgrade connection")
		return
	}
	defer func(conn *websocket.Conn) {
		err := conn.Close()
		if err != nil {
			fmt.Println("Close error:", err)
		}
	}(conn)

	wsLog.Debugf("New WebSocket connection established")
	// 10分钟鉴权超时
	authTimeoutCtx, authTimeoutCancel := context.WithTimeout(r.Context(), 600*time.Second)
	go b.authTimeoutHandler(conn, authTimeoutCtx)
	tempDevice := &model.BirdDevice{
		WSConn: conn,
	}
	err = b.authHandler(tempDevice, authTimeoutCancel)
	if err != nil {
		wsLog.WithError(err).Errorf("Websocket auth failed")
		return
	}
	tempDevice.MsgCount = &atomic.Int64{}
	tempDevice.MsgQueueCount = &atomic.Int64{}
	tempDevice.PaperMessageQueue = make(chan *model.BirdPaperMessage, 10000)
	tempDevice.PaperMessageQueueCtx, tempDevice.PaperMessageQueueCancel = context.WithCancel(context.Background())
	defer tempDevice.PaperMessageQueueCancel()
	var oldDevice *model.BirdDevice
	b.bird.DeviceMapLock.Lock()
	if _, ok := b.bird.DeviceMap[tempDevice.SmartGuid]; ok {
		oldDevice = b.bird.DeviceMap[tempDevice.SmartGuid]
	}
	b.bird.DeviceMap[tempDevice.SmartGuid] = tempDevice
	b.bird.DeviceMapLock.Unlock()
	// 踢出老设备
	if oldDevice != nil {
		wsLog.WithField("smartGuid", oldDevice.SmartGuid).Debugf("Kick Old Device!")
		oldDevice.KickOld = true
		_ = oldDevice.WSConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = oldDevice.WSConn.Close()
	}

	// 主协程退出之后同时干掉MAP里的设备
	defer func(tempDevice *model.BirdDevice) {
		if tempDevice.KickOld {
			return
		}
		b.bird.DeviceMapLock.Lock()
		delete(b.bird.DeviceMap, tempDevice.SmartGuid)
		b.bird.DeviceMapLock.Unlock()
	}(tempDevice)

	// 写入消息
	go b.handlerWriteChan(tempDevice)
	// 读取消息
	for {
		// 读取消息
		_, message, err := conn.ReadMessage()
		if err != nil {
			wsLog.WithField("smartGuid", tempDevice.SmartGuid).WithError(err).Debugf("Read")
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				wsLog.WithError(err).Errorf("Read")
			}
			break
		}
		wsLog.WithField("smartGuid", tempDevice.SmartGuid).WithField("msg", string(message)).Debugf("Received")
		go b.handlerMessage(tempDevice, message)
	}
}

func (b *Bird) authTimeoutHandler(conn *websocket.Conn, ctx context.Context) {
	<-ctx.Done()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// 超时啦
		wsLog.Errorf("Connection timeout")
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = conn.Close()
	} else {
		wsLog.Debugf("Connection auth done")
	}
}

func (b *Bird) authHandler(device *model.BirdDevice, cancel context.CancelFunc) error {
	err := device.WSConn.WriteMessage(websocket.TextMessage, []byte(model.BIRD_MSG_START+"{\"command\":0}"+model.BIRD_MSG_END))
	if err != nil {
		return err
	}
	for {
		// 读取消息
		_, msg, err := device.WSConn.ReadMessage()
		if err != nil {
			wsLog.WithField("smartGuid", "TEMP_DEVICE").WithError(err).Debugf("Read")
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				wsLog.WithError(err).Errorf("Read")
			}
			return err
		}
		wsLog.WithField("smartGuid", "TEMP_DEVICE").WithField("msg", string(msg)).Debugf("Received")
		if !strings.HasPrefix(string(msg), "MAC "+model.BIRD_MSG_START) || !strings.HasSuffix(string(msg), model.BIRD_MSG_END) {
			wsLog.WithField("smartGuid", "TEMP_DEVICE").WithField("msg", string(msg)).Errorf("AUTH1 INVALID MESSAGE")
			continue
		}

		// 获取纯净的JSON
		msgJson := strings.TrimPrefix(string(msg), "MAC "+model.BIRD_MSG_START)
		msgJson = strings.TrimSuffix(msgJson, model.BIRD_MSG_END)
		msgJson = strings.TrimSpace(msgJson)

		// 解析消息
		var authResp model.C2SAuth
		err = json.Unmarshal([]byte(msgJson), &authResp)
		if err != nil {
			return err
		}
		if authResp.Command != 1 {
			return errors.New("AUTH2 INVALID COMMAND")
		}

		device.JoinTime = time.Now()
		device.Bssid = authResp.Bssid
		device.SmartGuid = authResp.SmartGuid
		device.Led = authResp.Led
		device.Buz = authResp.Buz
		device.Speed = authResp.Speed
		device.Ver = authResp.Ver
		cancel()
		err = device.WSConn.WriteMessage(websocket.TextMessage, []byte(model.BIRD_MSG_START+"{\"result\":1,\"command\":1,\"smartGuid\":\""+device.SmartGuid+"\"}"+model.BIRD_MSG_END))
		if err != nil {
			return err
		}
		break
	}
	return nil
}

func (b *Bird) handlerWriteChan(device *model.BirdDevice) {
msgHandler:
	for {
		select {
		case <-device.PaperMessageQueueCtx.Done():
			// 退出
			break msgHandler
		case msg := <-device.PaperMessageQueue:
			// 有消息了
			device.MsgQueueCount.Add(-1)
			msgByte, err := b.makeMsg(device, msg)
			if err != nil {
				wsLog.WithField("smartGuid", device.SmartGuid).WithError(err).Errorf("makeMsg")
				continue
			}
			device.MsgCount.Add(1)
			err = device.WSConn.WriteMessage(websocket.TextMessage, msgByte)
			if err != nil {
				wsLog.WithField("smartGuid", device.SmartGuid).WithError(err).Errorf("Write")
				break msgHandler
			}
		}
	}
	_ = device.WSConn.Close()
}

func (b *Bird) makeMsg(device *model.BirdDevice, msg *model.BirdPaperMessage) ([]byte, error) {
	s2cPM := model.S2CPaperMessage{}
	s2cPM.Command = 3
	s2cPM.MsgType = 1
	s2cPM.PkgNo = msg.PaperNo
	s2cPM.PkgCount = msg.PaperCount
	s2cPM.PrintID = msg.PaperID
	s2cPM.PrintTableID = msg.PaperID
	if msg.Important {
		s2cPM.Priority = 1
	}
	s2cPMC := model.S2CPaperMessageContent{}
	for _, v := range msg.Content {
		s2cPMCTL := model.S2CPaperMessageContentTextList{}
		if v.Bold {
			s2cPMCTL.Bold = 1
		}
		s2cPMCTL.FontSize = v.FontSize
		if v.Content != "" {
			s2cPMCTL.Basetext = base64.StdEncoding.EncodeToString([]byte(v.Content))
		}
		if v.Type == 1 {
			// 文字
			s2cPMCTL.PrintType = 1
		} else if v.Type == 2 {
			// 图片
			s2cPMCTL.PrintType = 5
		} else if v.Type == 3 {
			// 图标
			s2cPMCTL.PrintType = 4
			s2cPMCTL.IconID = v.IconID
		} else {
			return nil, errors.New("unknown content type")
		}
		s2cPMC.TextList = append(s2cPMC.TextList, s2cPMCTL)
	}
	s2cPM.Content = s2cPMC
	msgByte, err := json.Marshal(s2cPM)
	if err != nil {
		return nil, err
	}
	return []byte(model.BIRD_MSG_START + string(msgByte) + model.BIRD_MSG_END), nil
}

func (b *Bird) handlerMessage(device *model.BirdDevice, msg []byte) {
	// 客户端上报设备信息
	if strings.HasPrefix(string(msg), "PULL "+model.BIRD_MSG_START) && strings.HasSuffix(string(msg), model.BIRD_MSG_END) {
		msgJson := strings.TrimPrefix(string(msg), "PULL "+model.BIRD_MSG_START)
		msgJson = strings.TrimSuffix(msgJson, model.BIRD_MSG_END)
		msgJson = strings.TrimSpace(msgJson)

		var msgData model.C2SReportDeviceInfo
		err := json.Unmarshal([]byte(msgJson), &msgData)
		if err != nil {
			wsLog.WithField("smartGuid", device.SmartGuid).WithError(err).Errorf("Unmarshal")
			return
		}

		if msgData.Command == 2 {
			// 上报设备LED、蜂鸣器状态
			device.Led = msgData.Led
			device.Buz = msgData.Buz
			device.Speed = msgData.Speed
			device.LanguageId = msgData.LanguageId
		} else if msgData.Command == 3 {
			// 上报纸条打印状态
			// {"command":3,"msgType":1,"printTableID":63471327,"packageCount":1,"packageNo":1,"result":1}
		} else if msgData.Command == 4 {
			// 上报旅程信息
			device.Trip = msgData.Trip
		} else if msgData.Command == 88 {
			// 上报打印设备是否忙碌
			device.Busy = msgData.Busy
			err = device.WSConn.WriteMessage(websocket.TextMessage, []byte(model.BIRD_MSG_START+"{\"command\":88,\"busy\":"+strconv.FormatInt(int64(msgData.Busy), 10)+",\"result\":1}"+model.BIRD_MSG_END))
			if err != nil {
				wsLog.WithError(err).Errorf("Send")
				return
			}
		} else {
			wsLog.WithField("smartGuid", device.SmartGuid).WithField("msg", string(msg)).Errorf("Unknown command")
		}
	} else {
		wsLog.WithField("smartGuid", device.SmartGuid).WithField("msg", string(msg)).Errorf("Unknown message")
	}
}
