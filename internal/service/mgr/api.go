package mgr

import "C"
import (
	"connectrpc.com/connect"
	"context"
	"errors"
	"fmt"
	v1 "github.com/flyqie/memobird-priv/gen/memobird/v1"
	"github.com/flyqie/memobird-priv/internal/helper"
	"github.com/flyqie/memobird-priv/internal/model"
	"github.com/flyqie/memobird-priv/internal/pkg/log"
	"strconv"
	"time"
)

var apiServerLog log.Logger

func init() {
	apiServerLog = log.Get("apiServer")
}

type ApiServer struct {
	Bird *model.Bird
}

func (s *ApiServer) ListOnlineDevices(ctx context.Context, req *connect.Request[v1.ListOnlineDevicesRequest]) (*connect.Response[v1.ListOnlineDevicesResponse], error) {
	var devices []string
	s.Bird.DeviceMapLock.RLock()
	defer s.Bird.DeviceMapLock.RUnlock()
	for k := range s.Bird.DeviceMap {
		devices = append(devices, k)
	}
	return connect.NewResponse(&v1.ListOnlineDevicesResponse{
		OnlineDevices: devices,
	}), nil
}

func (s *ApiServer) DeviceDetail(ctx context.Context, req *connect.Request[v1.GenericRequest]) (*connect.Response[v1.DeviceDetailResponse], error) {
	s.Bird.DeviceMapLock.RLock()
	defer s.Bird.DeviceMapLock.RUnlock()
	if v, ok := s.Bird.DeviceMap[req.Msg.SmartGuid]; ok {
		resp := &v1.DeviceDetailResponse{}
		resp.SmartGuid = v.SmartGuid
		resp.Bssid = v.Bssid
		resp.Led = int32(v.Led)
		resp.Buz = int32(v.Buz)
		resp.Speed = int64(v.Speed)
		resp.Ver = strconv.FormatInt(v.Ver, 10)
		resp.LanguageId = int32(v.LanguageId)
		resp.Busy = int32(v.Busy)
		resp.Trip = v.Trip
		resp.JoinTime = v.JoinTime.Unix()
		resp.MsgCount = uint64(v.MsgCount.Load())
		resp.MsgQueueCount = uint64(v.MsgQueueCount.Load())
		resp.DeviceStatus = v1.DeviceDetailResponse_NORMAL
		resp.Model = v.Model
		return connect.NewResponse(resp), nil
	} else {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
}

func (s *ApiServer) KickDevice(ctx context.Context, req *connect.Request[v1.GenericRequest]) (*connect.Response[v1.GenericResponse], error) {
	s.Bird.DeviceMapLock.RLock()
	defer s.Bird.DeviceMapLock.RUnlock()
	if v, ok := s.Bird.DeviceMap[req.Msg.SmartGuid]; ok {
		if v.WSConn != nil {
			err := v.WSConn.Close()
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

func (s *ApiServer) SetDevice(ctx context.Context, req *connect.Request[v1.SetDeviceRequest]) (*connect.Response[v1.GenericResponse], error) {
	/*
		s.Bird.DeviceMapLock.RLock()
		defer s.Bird.DeviceMapLock.RUnlock()
		if v, ok := s.Bird.DeviceMap[req.Msg.SmartGuid]; ok {
			if v.WSConn != nil {
				err := v.WSConn.WriteMessage(websocket.TextMessage, []byte(model.BIRD_MSG_START+"{\"command\":2,\"led\":"+strconv.FormatInt(int64(req.Msg.Led), 10)+",\"buz\":"+strconv.FormatInt(int64(req.Msg.Buz), 10)+",\"speed\":"+strconv.FormatInt(req.Msg.Speed, 10)+"}"+model.BIRD_MSG_END))
				if err != nil {
					return nil, helper.WrapConnectErrorDetail(connect.NewError(connect.CodeInternal, errors.New(model.DeviceSendFailed)), &v1.ErrDetail{
						Message: err.Error(),
					})
				}
			}
			return connect.NewResponse(&v1.GenericResponse{}), nil
		} else {
			return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
		}
	*/
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("unimplemented"))
}

func (s *ApiServer) SendMsg(ctx context.Context, req *connect.Request[v1.SendMsgRequest]) (*connect.Response[v1.GenericResponse], error) {
	s.Bird.DeviceMapLock.RLock()
	defer s.Bird.DeviceMapLock.RUnlock()
	if v, ok := s.Bird.DeviceMap[req.Msg.SmartGuid]; ok {
		if v.PaperMessageQueue != nil {
			// 获取总大小用来分包
			var baseTextLen int64 = 0
			for _, _v := range req.Msg.Content.Content {
				if _v.Type == v1.SendMsgRequestContentContent_TEXT || _v.Type == v1.SendMsgRequestContentContent_IMAGE {
					baseTextLen += int64(len(_v.Content.(*v1.SendMsgRequestContentContent_Text).Text))
				}
			}
			var pkgCount int64 = 1
			var pkgNo int64 = 0
			if baseTextLen > 25600 {
				// 需要分包
				pkgCount = baseTextLen / 25600
				if baseTextLen%25600 > 0 {
					pkgCount++
				}
			}
			var paperID int64
			if req.Msg.Content.PaperID > 0 {
				paperID = req.Msg.Content.PaperID
			} else {
				paperID = time.Now().Unix()
			}
			var papers []*model.BirdPaperMessage
			var contentNo = 0
			for {
				if contentNo > (len(req.Msg.Content.Content) - 1) {
					break
				}
				pkgNo++
				paperMsg := model.BirdPaperMessage{
					Important:  req.Msg.Content.Important,
					PaperCount: pkgCount,
					PaperNo:    pkgNo,
				}
				paperMsg.PaperID = paperID
				for {
					if contentNo > (len(req.Msg.Content.Content) - 1) {
						break
					}
					_v := req.Msg.Content.Content[contentNo]
					paperMsgContent := model.BirdPaperMessageContent{
						FontSize: int(_v.FontSize),
						Bold:     _v.Bold,
					}
					if _v.Type == v1.SendMsgRequestContentContent_TEXT {
						paperMsgContent.Type = 1
					} else if _v.Type == v1.SendMsgRequestContentContent_IMAGE {
						paperMsgContent.Type = 2
					} else if _v.Type == v1.SendMsgRequestContentContent_ICON {
						paperMsgContent.Type = 3
					}
					var isContentEnd = true
					switch _v.Content.(type) {
					case *v1.SendMsgRequestContentContent_Text:
						contentBytes := _v.Content.(*v1.SendMsgRequestContentContent_Text).Text
						paperMsgContent.Content = string(_v.Content.(*v1.SendMsgRequestContentContent_Text).Text)
						if len(contentBytes) > 25600 {
							isContentEnd = false
							paperMsgContent.Content = string(contentBytes[:25600])
							_v.Content.(*v1.SendMsgRequestContentContent_Text).Text = contentBytes[25600:]
						} else {
							paperMsgContent.Content = string(contentBytes)
						}
					case *v1.SendMsgRequestContentContent_IconID:
						paperMsgContent.IconID = _v.Content.(*v1.SendMsgRequestContentContent_IconID).IconID
					}
					paperMsg.Content = append(paperMsg.Content, paperMsgContent)
					if isContentEnd {
						contentNo++
					} else {
						break
					}
				}
				papers = append(papers, &paperMsg)
			}

			if int64(len(papers)) > pkgCount || pkgNo > pkgCount {
				return nil, helper.WrapConnectErrorDetail(connect.NewError(connect.CodeInternal, errors.New(model.DeviceSendFailed)), &v1.ErrDetail{
					Message: fmt.Sprintf("pkgNo:%d, pkgCount:%d, len(papers):%d", pkgNo, pkgCount, len(papers)),
				})
			}

			if len(papers) > 1 {
				// 需要分包,分包需要写锁保证不会有其他消息插入
				v.PaperMessageQueuePkgLock.Lock()
				defer v.PaperMessageQueuePkgLock.Unlock()
			} else {
				v.PaperMessageQueuePkgLock.RLock()
				defer v.PaperMessageQueuePkgLock.RUnlock()
			}
			for _, _v := range papers {
				// 插入消息队列
				v.PaperMessageQueue <- _v
				// 更新计量
				v.MsgQueueCount.Add(1)
			}
		}
		return connect.NewResponse(&v1.GenericResponse{}), nil
	} else {
		return nil, connect.NewError(connect.CodeInternal, errors.New(model.DeviceNotFound))
	}
}
