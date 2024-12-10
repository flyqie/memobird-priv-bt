package mgr

import (
	"bytes"
	"connectrpc.com/connect"
	"context"
	"errors"
	v1 "github.com/flyqie/memobird-priv-bt/gen/memobird/v1"
	"github.com/flyqie/memobird-priv-bt/internal/helper"
	"github.com/flyqie/memobird-priv-bt/internal/helper/birdproto"
	"github.com/flyqie/memobird-priv-bt/internal/model"
	"github.com/flyqie/memobird-priv-bt/internal/pkg/log"
	"github.com/sergeymakinen/go-bmp"
	"golang.org/x/image/draw"
	"golang.org/x/sys/unix"
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

type ApiServer struct {
	Bird *model.Bird
}

func (s *ApiServer) KickDevice(ctx context.Context, req *connect.Request[v1.GenericRequest]) (*connect.Response[v1.GenericResponse], error) {
	if req.Msg.SmartGuid == "" {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
	// 转换为全小写
	req.Msg.SmartGuid = strings.ToLower(req.Msg.SmartGuid)

	s.Bird.DeviceMapLock.RLock()
	defer s.Bird.DeviceMapLock.RUnlock()
	if v, ok := s.Bird.DeviceMap[req.Msg.SmartGuid]; ok {
		if v.FD > 0 {
			err := unix.Close(v.FD)
			if err != nil {
				return nil, helper.WrapConnectErrorDetail(connect.NewError(connect.CodeInternal, errors.New(model.DeviceKickFailed)), &v1.ErrDetail{
					Message: err.Error(),
				})
			}
		}
		return connect.NewResponse(&v1.GenericResponse{}), nil
	} else {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
}

func (s *ApiServer) ConnectDevice(ctx context.Context, req *connect.Request[v1.ConnectDeviceRequest]) (*connect.Response[v1.GenericResponse], error) {
	if req.Msg.SmartGuid == "" {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
	if _, err := net.ParseMAC(req.Msg.SmartGuid); err == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
	if len(req.Msg.SmartGuid) != 17 {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
	// 转换为全小写
	req.Msg.SmartGuid = strings.ToLower(req.Msg.SmartGuid)

	s.Bird.DeviceMapLock.RLock()
	defer s.Bird.DeviceMapLock.RUnlock()
	if _, ok := s.Bird.DeviceMap[req.Msg.SmartGuid]; ok {
		s.Bird.DeviceMapLock.RUnlock()
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceAlreadyConnected))
	} else {
		s.Bird.DeviceMapLock.RUnlock()
	}
	s.Bird.DeviceTempMapLock.RLock()
	defer s.Bird.DeviceTempMapLock.RUnlock()
	if _, ok := s.Bird.DeviceTempMap[req.Msg.SmartGuid]; ok {
		s.Bird.DeviceTempMapLock.RUnlock()
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceAlreadyConnected))
	} else {
		s.Bird.DeviceMapLock.RUnlock()
	}
	s.Bird.DeviceMapLock.Lock()
	s.Bird.DeviceTempMap[req.Msg.SmartGuid] = time.Now()
	s.Bird.DeviceMapLock.Unlock()
	s.Bird.BtMgrChan <- req.Msg.SmartGuid
	return connect.NewResponse(&v1.GenericResponse{}), nil
}

func (s *ApiServer) ListOnlineDevices(ctx context.Context, req *connect.Request[v1.ListOnlineDevicesRequest]) (*connect.Response[v1.ListOnlineDevicesResponse], error) {
	var devices []string
	s.Bird.DeviceMapLock.RLock()
	for k := range s.Bird.DeviceMap {
		devices = append(devices, k)
	}
	s.Bird.DeviceMapLock.RUnlock()
	s.Bird.DeviceTempMapLock.RLock()
	for k := range s.Bird.DeviceTempMap {
		devices = append(devices, k)
	}
	s.Bird.DeviceTempMapLock.RUnlock()
	// 去重
	var result []string
	for _, v := range devices {
		var found bool
		for _, rv := range result {
			if rv == v {
				found = true
				break
			}
		}
		if !found {
			result = append(result, v)
		}
	}
	return connect.NewResponse(&v1.ListOnlineDevicesResponse{
		OnlineDevices: result,
	}), nil
}

func (s *ApiServer) DeviceDetail(ctx context.Context, req *connect.Request[v1.GenericRequest]) (*connect.Response[v1.DeviceDetailResponse], error) {
	if req.Msg.SmartGuid == "" {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
	// 转换为全小写
	req.Msg.SmartGuid = strings.ToLower(req.Msg.SmartGuid)

	s.Bird.DeviceMapLock.RLock()
	defer s.Bird.DeviceMapLock.RUnlock()
	if v, ok := s.Bird.DeviceMap[req.Msg.SmartGuid]; ok {
		resp := &v1.DeviceDetailResponse{}
		resp.SmartGuid = v.SmartGuid
		resp.Bssid = v.Bssid
		if v.Led == 1 {
			resp.Led = true
		}
		switch v.Deep {
		case 5:
			resp.Deep = v1.DeviceDetailResponse_DEEP_LIGHT
		case 7:
			resp.Deep = v1.DeviceDetailResponse_DEEP_DEFAULT
		case 9:
			resp.Deep = v1.DeviceDetailResponse_DEEP_DARK
		}
		switch v.AutoShutdown {
		case 10:
			resp.AutoShutdown = v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_10
		case 30:
			resp.AutoShutdown = v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_30
		case 60:
			resp.AutoShutdown = v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_60
		case 120:
			resp.AutoShutdown = v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_120
		case 180:
			resp.AutoShutdown = v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_180
		case 240:
			resp.AutoShutdown = v1.DeviceDetailResponse_AUTO_SHUTDOWN_TIME_240
		}
		resp.Ver = strconv.FormatInt(v.Ver, 10)
		resp.Elec = int32(v.Elec)
		resp.JoinTime = v.JoinTime.Unix()
		resp.MsgCount = uint64(v.MsgCount.Load())
		resp.MsgQueueCount = uint64(v.MsgQueueCount.Load())
		resp.DeviceStatus = v1.DeviceDetailResponse_DEVICE_STATUS_NORMAL
		resp.Model = v.Model
		if v.PaperMessageCallbackQueue != nil {
			resp.CbCheck = true
		}
		return connect.NewResponse(resp), nil
	} else {
		if _, ok := s.Bird.DeviceTempMap[req.Msg.SmartGuid]; ok {
			// 正在连接中的设备
			return connect.NewResponse(&v1.DeviceDetailResponse{
				SmartGuid:    req.Msg.SmartGuid,
				DeviceStatus: v1.DeviceDetailResponse_DEVICE_STATUS_CONNECTING,
			}), nil
		} else {
			return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
		}
	}
}

func (s *ApiServer) SetDevice(ctx context.Context, req *connect.Request[v1.SetDeviceRequest]) (*connect.Response[v1.GenericResponse], error) {
	if req.Msg.SmartGuid == "" {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
	// 转换为全小写
	req.Msg.SmartGuid = strings.ToLower(req.Msg.SmartGuid)

	s.Bird.DeviceMapLock.RLock()
	defer s.Bird.DeviceMapLock.RUnlock()
	if v, ok := s.Bird.DeviceMap[req.Msg.SmartGuid]; ok {
		if v.FD > 0 {
			var msgBytes []byte
			switch setting := req.Msg.Setting.(type) {
			case *v1.SetDeviceRequest_Led:
				_led := 0
				if setting.Led {
					_led = 1
				}
				msgBytes = birdproto.SetDevice(3, _led)
			case *v1.SetDeviceRequest_Deep:
				_deep := 7
				switch setting.Deep {
				case v1.SetDeviceRequest_DEEP_LIGHT:
					_deep = 5
				case v1.SetDeviceRequest_DEEP_DEFAULT:
					_deep = 7
				case v1.SetDeviceRequest_DEEP_DARK:
					_deep = 9
				}
				msgBytes = birdproto.SetDevice(2, _deep)
			case *v1.SetDeviceRequest_AutoShutdown:
				_autoShutdown := 10
				switch setting.AutoShutdown {
				case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_10:
					_autoShutdown = 10
				case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_30:
					_autoShutdown = 30
				case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_60:
					_autoShutdown = 60
				case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_120:
					_autoShutdown = 120
				case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_180:
					_autoShutdown = 180
				case v1.SetDeviceRequest_AUTO_SHUTDOWN_TIME_240:
					_autoShutdown = 240
				}
				msgBytes = birdproto.SetDevice(19, _autoShutdown)
			}
			if _, err := unix.Write(v.FD, msgBytes); err != nil {
				return nil, helper.WrapConnectErrorDetail(connect.NewError(connect.CodeInternal, errors.New(model.DeviceSendFailed)), &v1.ErrDetail{
					Message: err.Error(),
				})
			}
		}
		return connect.NewResponse(&v1.GenericResponse{}), nil
	} else {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
}

func (s *ApiServer) SendMsg(ctx context.Context, req *connect.Request[v1.SendMsgRequest]) (*connect.Response[v1.SendMsgResponse], error) {
	if req.Msg.SmartGuid == "" {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
	// 转换为全小写
	req.Msg.SmartGuid = strings.ToLower(req.Msg.SmartGuid)

	s.Bird.DeviceMapLock.RLock()
	defer s.Bird.DeviceMapLock.RUnlock()
	if v, ok := s.Bird.DeviceMap[req.Msg.SmartGuid]; ok {
		var paperID int64 = 0
		if v.PaperMessageQueue != nil {
			var pkgCount int64 = 1
			var pkgNo int64 = 0
			var papers []*model.BirdPaperMessage
			var contentNo = 0
			paperID = time.Now().UnixMilli()
			for {
				if contentNo > (len(req.Msg.Content.Content) - 1) {
					break
				}
				pkgNo++
				paperMsgContent := model.BirdPaperMessageContent{}
				_paperData := req.Msg.Content.Content[contentNo]
				_maxLen := 1024
				if _paperData.Type == v1.SendMsgRequestContentContent_CONTENT_TEXT {
					_maxLen = 500
				}
				if _paperData.Type == v1.SendMsgRequestContentContent_CONTENT_TEXT {
					paperMsgContent.Type = 1
				} else if _paperData.Type == v1.SendMsgRequestContentContent_CONTENT_IMAGE {
					paperMsgContent.Type = 2
				} else if _paperData.Type == v1.SendMsgRequestContentContent_CONTENT_ICON {
					paperMsgContent.Type = 3
				} else if _paperData.Type == v1.SendMsgRequestContentContent_CONTENT_QR {
					paperMsgContent.Type = 4
				}
				isContentEnd := true
				if len(_paperData.BaseText) > _maxLen {
					isContentEnd = false
					paperMsgContent.Content = string(_paperData.BaseText[:_maxLen])
					req.Msg.Content.Content[contentNo].BaseText = _paperData.BaseText[(_maxLen - 1):]
				} else {
					paperMsgContent.Content = string(_paperData.BaseText)
				}
				if paperMsgContent.Type == 1 && !_paperData.NoWrap {
					paperMsgContent.Content += "\n"
				}
				if isContentEnd {
					contentNo++
				}
				paperMsg := model.BirdPaperMessage{
					PaperCount: 0,
					PaperNo:    pkgNo,
					Content:    paperMsgContent,
					PaperID:    paperID,
				}
				papers = append(papers, &paperMsg)
			}
			papersLen := len(papers)
			for k := range papers {
				papers[k].PaperCount = int64(papersLen)
			}

			if len(papers) > 1 {
				// 分包需要写锁保证不会有其他消息插入
				v.PaperMessageQueuePkgLock.Lock()
				defer v.PaperMessageQueuePkgLock.Unlock()
			} else {
				v.PaperMessageQueuePkgLock.RLock()
				defer v.PaperMessageQueuePkgLock.RUnlock()
			}
			apiServerLog.WithField("smartGuid", v.SmartGuid).WithField("pkgCount", pkgCount).Debugf("SendMsg")
			for _, _v := range papers {
				// 插入消息队列
				v.PaperMessageQueue <- _v
				// 更新计量
				v.MsgQueueCount.Add(1)
			}
		}
		return connect.NewResponse(&v1.SendMsgResponse{
			PaperID: paperID,
		}), nil
	} else {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
}

func (s *ApiServer) UnlockDeviceMessageCallback(ctx context.Context, req *connect.Request[v1.GenericRequest]) (*connect.Response[v1.GenericResponse], error) {
	if req.Msg.SmartGuid == "" {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
	// 转换为全小写
	req.Msg.SmartGuid = strings.ToLower(req.Msg.SmartGuid)

	s.Bird.DeviceMapLock.RLock()
	defer s.Bird.DeviceMapLock.RUnlock()
	if v, ok := s.Bird.DeviceMap[req.Msg.SmartGuid]; ok {
		if v.PaperMessageCallbackQueue != nil {
			v.PaperMessageCallbackQueue <- "RPC_UnlockDeviceMessageCallback"
		}
		return connect.NewResponse(&v1.GenericResponse{}), nil
	} else {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
}

func (s *ApiServer) MakeMonoBmp(ctx context.Context, req *connect.Request[v1.MakeMonoBmpRequest]) (*connect.Response[v1.MakeMonoBmpResponse], error) {
	makeBmpFunc := func(input []byte) ([]byte, error) {
		reader := bytes.NewReader(input)
		srcImg, _, err := image.Decode(reader)
		if err != nil {
			return nil, err
		}

		bounds := srcImg.Bounds()
		var scaledImg image.Image
		const targetWidth = 384
		if bounds.Dx() > targetWidth {
			scaleFactor := float64(targetWidth) / float64(bounds.Dx())
			targetHeight := int(float64(bounds.Dy()) * scaleFactor)
			resized := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
			draw.CatmullRom.Scale(resized, resized.Bounds(), srcImg, bounds, draw.Over, nil)
			scaledImg = resized
		} else {
			scaledImg = srcImg
		}

		newBounds := scaledImg.Bounds()
		palette := []color.Color{color.White, color.Black}
		palettedImg := image.NewPaletted(newBounds, palette)
		draw.Draw(palettedImg, newBounds, scaledImg, newBounds.Min, draw.Src)

		for y := newBounds.Min.Y; y < newBounds.Max.Y; y++ {
			for x := newBounds.Min.X; x < newBounds.Max.X; x++ {
				c := scaledImg.At(x, y)
				grayColor := color.GrayModel.Convert(c).(color.Gray)
				if grayColor.Y > 128 {
					palettedImg.Set(x, y, color.White)
				} else {
					palettedImg.Set(x, y, color.Black)
				}
			}
		}

		var output bytes.Buffer
		err = bmp.Encode(&output, palettedImg)
		if err != nil {
			return nil, err
		}

		return output.Bytes(), nil
	}

	output, err := makeBmpFunc(req.Msg.Image)
	if err != nil {
		return nil, helper.WrapConnectErrorDetail(connect.NewError(connect.CodeInternal, errors.New(model.MakeBmpFailed)), &v1.ErrDetail{
			Message: err.Error(),
		})
	}
	return connect.NewResponse(&v1.MakeMonoBmpResponse{
		Bmp: output,
	}), nil
}
