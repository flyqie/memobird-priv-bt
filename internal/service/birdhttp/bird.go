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
		/*
			if r.URL.Query().Get("version") == "Memobird2" {
				// 我们只支持Memobird2
				_, _ = w.Write([]byte(`{"version":8,"path":"http:\/\/im.memobird.cn\/SWPackage\/Memobird2_FW_V0.8.bin","releaseNote":"MS7Txbuv1Ma2y8GsvdO7+tbGOw0K","releaseNoteEnglish":"MS5PcHRpbWl6ZSB0aGUgY2xvdWQgY29ubmVjdGlvbjsNCg=="}`))
				return
			}
		*/
		_, _ = w.Write([]byte(``))
	} else if dataType == "getallserverip" {
		// 获取WS服务器IP列表
		_, _ = w.Write([]byte(`{"serverips":[{"ip":"` + b.ip + `","port":81}]}`))
	} else if dataType == "preloadimg" {
		// ICON更新
		_, _ = w.Write([]byte(``))
	} else if dataType == "getserveripandport" {
		// 根据smartguid获取最后的WS服务器地址信息?
		// 目前不判断, 统一走到固定地址
		_, _ = w.Write([]byte(`{"ip":"` + b.ip + `","port":81}`))
	} else {
		_, _ = w.Write([]byte(``))
	}
}
