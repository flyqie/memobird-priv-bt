package birdhttp

import (
	"github.com/flyqie/memobird-priv/internal/pkg/log"
	"net/http"
)

var httpLog log.Logger

func init() {
	httpLog = log.Get("http")
}

type Bird struct {
	ip string
}

func NewHTTP(ip string) *Bird {
	return &Bird{
		ip: ip,
	}
}

func (b *Bird) Bind(mux *http.ServeMux) {
	mux.HandleFunc("/ashx/ClientInterface.ashx", b.clientInterfaceHandler)
}

func (b *Bird) clientInterfaceHandler(w http.ResponseWriter, r *http.Request) {
	// 获取GET参数
	dataType := r.URL.Query().Get("DataType")
	if dataType == "checksw" {
		// 固件信息获取
		/**
		{"version":8,"path":"http:\/\/im.memobird.cn\/SWPackage\/Memobird2_FW_V0.8.bin","releaseNote":"MS7Txbuv1Ma2y8GsvdO7+tbGOw0K","releaseNoteEnglish":"MS5PcHRpbWl6ZSB0aGUgY2xvdWQgY29ubmVjdGlvbjsNCg=="}
		*/
		_, _ = w.Write([]byte(``))
	} else if dataType == "getallserverip" {
		// 获取WS服务器IP列表
		/**
		{"serverips":[{"ip":"139.217.201.114"},{"ip":"139.217.205.76"},{"ip":"139.219.226.188"}]}
		*/
		_, _ = w.Write([]byte(`{"serverips":[{"ip":"` + b.ip + `"}]}`))
	} else if dataType == "preloadimg" {
		// ICON更新
		/**
		{"path":"http://im.memobird.cn/PreloadImg/","updateTime":"636180833970000000","imgs":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,22,23,24,25,26,27,28,29,30,21,31,32,33,34,35,36,37,38,39,40,44,45,46,47,48,49,50,59,57]}

		$path/$imgs[$i].bmp
		*/
		_, _ = w.Write([]byte(``))
	} else if dataType == "getserveripandport" {
		// 根据smartguid获取最后的WS服务器地址信息?
		// 目前不判断, 统一走到固定地址
		/**
		{"ip":"139.219.226.188","port":4413}
		*/
		_, _ = w.Write([]byte(`{"ip":"` + b.ip + `","port":81}`))
	} else {
		_, _ = w.Write([]byte(``))
	}
}
