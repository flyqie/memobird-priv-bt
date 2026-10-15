package mgr

import (
	"bytes"
	"connectrpc.com/connect"
	"context"
	"errors"
	"fmt"
	v1 "github.com/flyqie/memobird-priv-bt/gen/memobird/v1"
	"github.com/flyqie/memobird-priv-bt/internal/helper"
	"github.com/flyqie/memobird-priv-bt/internal/helper/birdproto"
	"github.com/flyqie/memobird-priv-bt/internal/model"
	"github.com/flyqie/memobird-priv-bt/internal/pkg/log"
	"golang.org/x/image/draw"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net"
	"strconv"
	"strings"
	"time"
)

var apiServerLog log.Logger

func init() {
	apiServerLog = log.Get("apiServer")
}

// deviceRefreshTimeout 是 DeviceDetail(refresh=true) 等握手应答的超时。
// 官方 SDK 的握手超时是 15s，但那是冷启动场景；已连上的设备通常是毫秒级。
const deviceRefreshTimeout = 2 * time.Second

// kickConnTimeout 是 KickDevice 等连接协程退出的超时。
// 关掉 fd 之后收包协程应该立刻从 Read 返回，正常是毫秒级。
const kickConnTimeout = 2 * time.Second

type ApiServer struct {
	Bird *model.Bird
}

// normalizeSmartGuid 校验并归一化设备 GUID（本服务里就是蓝牙 MAC，统一转小写）。
// net.ParseMAC 也接受 "aabb.ccdd.eeff"，这里额外卡 17 位只认冒号分隔。
func normalizeSmartGuid(raw string) (string, error) {
	guid := strings.ToLower(strings.TrimSpace(raw))
	if len(guid) != 17 {
		return "", errors.New(model.InvalidSmartGuid)
	}
	if _, err := net.ParseMAC(guid); err != nil {
		return "", errors.New(model.InvalidSmartGuid)
	}
	return guid, nil
}

// invalidArgument 构造「请求参数不合法」错误。
func invalidArgument(code string) *connect.Error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(code))
}

// detailError 构造带 ErrDetail 的错误。
// message 用 model 里的错误码（接口契约），detail 放底层原因或具体的非法取值。
func detailError(code connect.Code, codeName string, cause error) *connect.Error {
	return helper.WrapConnectErrorDetail(
		connect.NewError(code, errors.New(codeName)),
		&v1.ErrDetail{Message: cause.Error()},
	)
}

func (s *ApiServer) KickDevice(ctx context.Context, req *connect.Request[v1.GenericRequest]) (*connect.Response[v1.GenericResponse], error) {
	guid, err := normalizeSmartGuid(req.Msg.SmartGuid)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	s.Bird.DeviceMapLock.RLock()
	device, ok := s.Bird.DeviceMap[guid]
	s.Bird.DeviceMapLock.RUnlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New(model.DeviceNotFound))
	}

	if err := device.Close(); err != nil {
		return nil, detailError(connect.CodeInternal, model.DeviceKickFailed, err)
	}
	// 等收包协程退出（它退出时会把设备从 DeviceMap 摘掉）。
	// 必须在 DeviceMapLock 外面等：收包协程的 defer 要拿写锁，持着读锁等就是死锁。
	if !device.WaitConnDone(kickConnTimeout) {
		apiServerLog.WithField("smartGuid", guid).Debug("wait for connection teardown timed out")
	}
	return connect.NewResponse(&v1.GenericResponse{}), nil
}

func (s *ApiServer) ConnectDevice(ctx context.Context, req *connect.Request[v1.ConnectDeviceRequest]) (*connect.Response[v1.GenericResponse], error) {
	guid, err := normalizeSmartGuid(req.Msg.SmartGuid)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	s.Bird.DeviceMapLock.RLock()
	_, connected := s.Bird.DeviceMap[guid]
	s.Bird.DeviceMapLock.RUnlock()
	if connected {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New(model.DeviceAlreadyConnected))
	}

	s.Bird.DeviceTempMapLock.RLock()
	_, connecting := s.Bird.DeviceTempMap[guid]
	s.Bird.DeviceTempMapLock.RUnlock()
	if connecting {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New(model.DeviceAlreadyConnected))
	}

	// 写的是 DeviceTempMap，必须用 DeviceTempMapLock。
	// 用错锁的话 Go 的 map 并发读写会直接 panic，且 recover 不掉。
	s.Bird.DeviceTempMapLock.Lock()
	s.Bird.DeviceTempMap[guid] = time.Now()
	s.Bird.DeviceTempMapLock.Unlock()

	apiServerLog.WithField("smartGuid", guid).Debug("queue device discovery")
	s.Bird.BtMgrChan <- guid
	return connect.NewResponse(&v1.GenericResponse{}), nil
}

func (s *ApiServer) ListOnlineDevices(ctx context.Context, req *connect.Request[v1.ListOnlineDevicesRequest]) (*connect.Response[v1.ListOnlineDevicesResponse], error) {
	// 两个 map 的 key 可能重复（连接中 / 已连接），用 set 去重但保留原有顺序
	var result []string
	seen := make(map[string]struct{})
	appendUnique := func(k string) {
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		result = append(result, k)
	}
	s.Bird.DeviceMapLock.RLock()
	for k := range s.Bird.DeviceMap {
		appendUnique(k)
	}
	s.Bird.DeviceMapLock.RUnlock()
	s.Bird.DeviceTempMapLock.RLock()
	for k := range s.Bird.DeviceTempMap {
		appendUnique(k)
	}
	s.Bird.DeviceTempMapLock.RUnlock()
	return connect.NewResponse(&v1.ListOnlineDevicesResponse{
		OnlineDevices: result,
	}), nil
}

func (s *ApiServer) DeviceDetail(ctx context.Context, req *connect.Request[v1.DeviceDetailRequest]) (*connect.Response[v1.DeviceDetailResponse], error) {
	guid, err := normalizeSmartGuid(req.Msg.SmartGuid)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	s.Bird.DeviceMapLock.RLock()
	device, ok := s.Bird.DeviceMap[guid]
	s.Bird.DeviceMapLock.RUnlock()
	if !ok {
		// 读 DeviceTempMap 要用它自己的锁
		s.Bird.DeviceTempMapLock.RLock()
		_, connecting := s.Bird.DeviceTempMap[guid]
		s.Bird.DeviceTempMapLock.RUnlock()
		if !connecting {
			return nil, connect.NewError(connect.CodeNotFound, errors.New(model.DeviceNotFound))
		}
		return connect.NewResponse(&v1.DeviceDetailResponse{
			SmartGuid:    guid,
			DeviceStatus: v1.DeviceDetailResponse_DEVICE_STATUS_CONNECTING,
		}), nil
	}

	// refresh 时补发一次握手并等应答，保证读到的是最新状态。
	// 注意这一步不能放在 DeviceMapLock 里：等应答最长要几百毫秒，
	// 持着读锁会把 ConnectDevice / HandleDevice 一起卡住。
	if req.Msg.Refresh {
		refreshed := device.RefreshState(deviceRefreshTimeout, func() error {
			return device.Write(birdproto.GetDevice())
		})
		if !refreshed {
			// 设备没应答（超时或已断连），退回缓存值，不算接口错误
			apiServerLog.WithField("smartGuid", guid).Debug("refresh device state timed out")
		}
	}

	snap := device.Snapshot()
	resp := &v1.DeviceDetailResponse{}
	resp.SmartGuid = device.SmartGuid
	resp.Bssid = device.Bssid
	resp.Led = snap.Led == 1
	resp.Deep = deepEnum(snap.Deep)
	resp.DeepRaw = int32(snap.Deep)
	resp.AutoShutdown = autoShutdownEnum(snap.AutoShutdown)
	resp.AutoShutdownMinutes = int32(snap.AutoShutdown)
	resp.Ver = strconv.FormatInt(snap.Ver, 10)
	resp.Elec = int32(snap.Elec)
	resp.JoinTime = device.JoinTime.Unix()
	resp.MsgCount = uint64(device.MsgCount.Load())
	resp.MsgQueueCount = uint64(device.MsgQueueCount.Load())
	resp.DeviceStatus = v1.DeviceDetailResponse_DEVICE_STATUS_NORMAL
	resp.Model = device.Model
	if device.PaperMessageCallbackQueue != nil {
		resp.CbCheck = true
	}
	return connect.NewResponse(resp), nil
}

// buildSettingFrame 把 SetDevice 请求里的设置项转成协议帧。
// 没指定设置项时返回 model.NoSettingSpecified。
func buildSettingFrame(req *v1.SetDeviceRequest) ([]byte, error) {
	switch setting := req.Setting.(type) {
	case *v1.SetDeviceRequest_Led:
		return birdproto.SetLed(setting.Led), nil

	case *v1.SetDeviceRequest_Deep:
		// 接口只暴露三档，协议本身支持 1-30
		deep := 7
		switch setting.Deep {
		case v1.SetDeviceRequest_DEEP_LIGHT:
			deep = 5
		case v1.SetDeviceRequest_DEEP_DARK:
			deep = 9
		}
		return birdproto.SetDeep(deep), nil

	case *v1.SetDeviceRequest_AutoShutdown:
		minutes := 10
		switch setting.AutoShutdown {
		case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_NONE:
			minutes = 0 // 协议里 0 = 不自动关机
		case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_30:
			minutes = 30
		case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_60:
			minutes = 60
		case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_120:
			minutes = 120
		case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_180:
			minutes = 180
		case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_240:
			minutes = 240
		}
		return birdproto.SetAutoShutdown(minutes), nil

	default:
		return nil, errors.New(model.NoSettingSpecified)
	}
}

func (s *ApiServer) SetDevice(ctx context.Context, req *connect.Request[v1.SetDeviceRequest]) (*connect.Response[v1.GenericResponse], error) {
	guid, err := normalizeSmartGuid(req.Msg.SmartGuid)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	msgBytes, err := buildSettingFrame(req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	s.Bird.DeviceMapLock.RLock()
	device, ok := s.Bird.DeviceMap[guid]
	s.Bird.DeviceMapLock.RUnlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New(model.DeviceNotFound))
	}
	if !device.IsOpen() {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New(model.DeviceConnNotReady))
	}

	// 走 BirdDevice.Write 串行化，避免和打印协程的帧写交错
	if err := device.Write(msgBytes); err != nil {
		return nil, detailError(connect.CodeUnavailable, model.DeviceSendFailed, err)
	}
	return connect.NewResponse(&v1.GenericResponse{}), nil
}

func (s *ApiServer) SendMsg(ctx context.Context, req *connect.Request[v1.SendMsgRequest]) (*connect.Response[v1.SendMsgResponse], error) {
	guid, err := normalizeSmartGuid(req.Msg.SmartGuid)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	s.Bird.DeviceMapLock.RLock()
	device, ok := s.Bird.DeviceMap[guid]
	s.Bird.DeviceMapLock.RUnlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New(model.DeviceNotFound))
	}
	if device.PaperMessageQueue == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New(model.DeviceConnNotReady))
	}

	paperID := time.Now().UnixMilli()

	// 打印前导：2 个 1024 字节空包，用于唤醒设备 / 清空缓冲。
	// 它们不带协议帧，也不计入 pkgCount / pkgNo。
	var papers []*model.BirdPaperMessage
	for i := 0; i < birdproto.WakeupPacketCount; i++ {
		papers = append(papers, &model.BirdPaperMessage{
			PaperID: paperID,
			Content: model.BirdPaperMessageContent{Type: 0},
		})
	}

	var pkgNo int64 = 0
	for contentNo := 0; contentNo < len(req.Msg.Content.Content); contentNo++ {
		seg := req.Msg.Content.Content[contentNo]

		// 文本段默认补一个换行符；换行符也占用本包字节数
		var suffix []byte
		if seg.Type == v1.SendMsgRequestContentContent_CONTENT_TEXT && !seg.NoWrap {
			suffix = []byte("\n")
		}

		content := model.BirdPaperMessageContent{
			Underline: seg.Underline,
			Bold:      seg.Bold,
			IconID:    seg.IconID,
		}

		// 按协议单包上限切分。文本必须按 GBK 字符边界切，
		// 否则汉字会被劈成两半导致乱码。
		var chunks [][]byte
		switch seg.Type {
		case v1.SendMsgRequestContentContent_CONTENT_TEXT:
			content.Type = 1
			chunks = birdproto.SplitGBKText(seg.BaseText, birdproto.MaxParamValueLen-len(suffix))
		case v1.SendMsgRequestContentContent_CONTENT_IMAGE:
			content.Type = 2
			chunks = birdproto.SplitPayload(seg.BaseText, birdproto.MaxParamValueLen)
			// 注意：官方 SDK 与 App 默认都不发 TAG 22(长图)，
			// 若实测图片打印异常，可尝试在这里置为 true。
			content.LongPic = false
		case v1.SendMsgRequestContentContent_CONTENT_ICON:
			content.Type = 3
			chunks = [][]byte{nil}
		case v1.SendMsgRequestContentContent_CONTENT_QR:
			content.Type = 4
			chunks = birdproto.SplitPayload(seg.BaseText, birdproto.MaxParamValueLen)
		default:
			return nil, detailError(connect.CodeInvalidArgument, model.UnknownContentType,
				fmt.Errorf("content type %d", seg.Type))
		}
		if len(chunks) == 0 {
			// 空内容段：仍然发一包，用于输出一个空行
			chunks = [][]byte{nil}
		}

		for i, chunk := range chunks {
			pkgNo++
			c := content
			c.Content = string(chunk)
			// 换行符只补在该内容段的最后一包
			if i == len(chunks)-1 && len(suffix) > 0 {
				c.Content += string(suffix)
			}
			papers = append(papers, &model.BirdPaperMessage{
				PaperID: paperID,
				PaperNo: pkgNo,
				Content: c,
			})
		}
	}
	if pkgNo == 0 {
		return nil, invalidArgument(model.PaperContentEmpty)
	}
	// 总包数不含前导空包
	for _, p := range papers[birdproto.WakeupPacketCount:] {
		p.PaperCount = pkgNo
	}

	// 先确认队列装得下整条任务。不能边塞边等：队列满时 send 会阻塞，
	// 而此时还持着锁，会把 ConnectDevice / KickDevice 一起拖住。
	if len(device.PaperMessageQueue)+len(papers) > cap(device.PaperMessageQueue) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New(model.DeviceQueueFull))
	}

	// 整条任务一次性入队，一律用写锁：上面的容量检查只在没有别的协程同时入队时
	// 才成立，读锁会允许多个 SendMsg 各自通过检查后一起挤爆队列。
	device.PaperMessageQueuePkgLock.Lock()
	defer device.PaperMessageQueuePkgLock.Unlock()

	apiServerLog.WithField("smartGuid", guid).WithField("pkgCount", pkgNo).Debug("enqueue paper message")
	for _, p := range papers {
		select {
		case device.PaperMessageQueue <- p:
			device.MsgQueueCount.Add(1)
		case <-device.PaperMessageQueueCtx.Done():
			// 设备入队途中断开，写协程已经退出，再塞就永远塞不进去了。
			// 此时可能已经有部分包进了队列，但设备正在断开，无所谓。
			return nil, connect.NewError(connect.CodeUnavailable, errors.New(model.DeviceSendFailed))
		}
	}

	return connect.NewResponse(&v1.SendMsgResponse{
		PaperID: paperID,
	}), nil
}

func (s *ApiServer) UnlockDeviceMessageCallback(ctx context.Context, req *connect.Request[v1.GenericRequest]) (*connect.Response[v1.GenericResponse], error) {
	guid, err := normalizeSmartGuid(req.Msg.SmartGuid)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	s.Bird.DeviceMapLock.RLock()
	device, ok := s.Bird.DeviceMap[guid]
	s.Bird.DeviceMapLock.RUnlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New(model.DeviceNotFound))
	}

	// 回调队列只在 MESSAGE_CB_CHECK=ENABLE 时才建，没开就没什么可解锁的
	if device.PaperMessageCallbackQueue != nil {
		device.PaperMessageCallbackQueue <- "RPC_UnlockDeviceMessageCallback"
	}
	return connect.NewResponse(&v1.GenericResponse{}), nil
}

// deepEnum 把设备上报的打印浓度(1-30)归到接口暴露的三档。
//
// 设备可以上报 1-30 之间任意值，直接 switch 会让没列举到的值落到枚举默认值
// (DEEP_DEFAULT)，看起来像"适中"；这里按最接近的一档归类。
func deepEnum(deep int) v1.DeviceDetailResponse_DeepType {
	switch {
	case deep <= 0:
		// 还没握过手，保持默认档
		return v1.DeviceDetailResponse_DEEP_DEFAULT
	case deep < 6: // 5(浅) 与 7(适中) 的中点
		return v1.DeviceDetailResponse_DEEP_LIGHT
	case deep < 9: // 7(适中) 与 9(深) 的中点
		return v1.DeviceDetailResponse_DEEP_DEFAULT
	default:
		return v1.DeviceDetailResponse_DEEP_DARK
	}
}

// autoShutdownEnum 把设备上报的自动关机分钟数归到接口暴露的档位。
// 0（协议里的"不自动关机"）现在有 AUTO_SHUTDOWN_TIME_NONE 对应，
// 其余值按最接近的一档归类；要精确值请用 DeviceDetailResponse.autoShutdownMinutes。
func autoShutdownEnum(minutes int) v1.DeviceDetailResponse_AutoShutdownType {
	switch {
	case minutes <= 0:
		return v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_NONE
	case minutes <= 20: // <=10，以及 10/30 的中点
		return v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_10
	case minutes <= 45: // 30/60 中点
		return v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_30
	case minutes <= 90: // 60/120 中点
		return v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_60
	case minutes <= 150: // 120/180 中点
		return v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_120
	case minutes <= 210: // 180/240 中点
		return v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_180
	default:
		return v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_240
	}
}

// 打印机纸宽（像素）。G4 / G5 / Teen HD 是 576，其余是 384。
const (
	paperWidth384 = 384
	paperWidth576 = 576
)

func (s *ApiServer) MakeMonoBmp(ctx context.Context, req *connect.Request[v1.MakeMonoBmpRequest]) (*connect.Response[v1.MakeMonoBmpResponse], error) {
	srcImg, _, err := image.Decode(bytes.NewReader(req.Msg.Image))
	if err != nil {
		// 图是调用方给的，解不开是它的问题
		return nil, detailError(connect.CodeInvalidArgument, model.InvalidImage, err)
	}

	bounds := srcImg.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW <= 0 || srcH <= 0 {
		return nil, detailError(connect.CodeInvalidArgument, model.InvalidImage, errors.New("image has empty size"))
	}

	// 目标纸宽：0 = 默认 384；超过 576 按 576 处理
	targetWidth := int(req.Msg.Width)
	if targetWidth <= 0 {
		targetWidth = paperWidth384
	}
	if targetWidth > paperWidth576 {
		targetWidth = paperWidth576
	}

	// 等比缩放，只缩不放
	scaledW, scaledH := srcW, srcH
	if srcW > targetWidth {
		scaledW = targetWidth
		scaledH = int(float64(srcH) * float64(targetWidth) / float64(srcW))
		if scaledH < 1 {
			scaledH = 1
		}
	}

	// 画布：居中时补到整幅纸宽，否则就是缩放后的宽度
	canvasW := scaledW
	if req.Msg.Center && scaledW < targetWidth {
		canvasW = targetWidth
	}
	canvas := image.NewRGBA(image.Rect(0, 0, canvasW, scaledH))
	// 先铺白底：这样带透明通道的 PNG 不会变成黑块
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)

	offsetX := (canvasW - scaledW) / 2
	dstRect := image.Rect(offsetX, 0, offsetX+scaledW, scaledH)
	if scaledW == srcW && scaledH == srcH {
		// 尺寸没变就别重采样，避免 CatmullRom 引入无谓的模糊
		draw.Draw(canvas, dstRect, srcImg, bounds.Min, draw.Over)
	} else {
		draw.CatmullRom.Scale(canvas, dstRect, srcImg, bounds, draw.Over, nil)
	}

	width, height := canvasW, scaledH

	// 复刻官方 convertGray -> doDither 的处理链（libBmpJniUtilDither.so）
	pix := make([]int32, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := canvas.At(x, y).RGBA()
			pix[y*width+x] = int32(r>>8)<<16 | int32(g>>8)<<8 | int32(b>>8)
		}
	}
	birdproto.ARGBToGray(pix)
	birdproto.Dither(pix, width, height)

	mono := birdproto.NewMonoBitmap(width, height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Dither 输出只有 0x000000(黑) 和 0xFFFFFF(白)
			mono.SetWhite(x, y, pix[y*width+x] != 0)
		}
	}

	return connect.NewResponse(&v1.MakeMonoBmpResponse{
		Bmp: mono.EncodeBMP(),
	}), nil
}
