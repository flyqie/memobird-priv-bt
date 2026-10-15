package helper

import (
	"connectrpc.com/connect"
	"github.com/flyqie/memobird-priv-bt/internal/pkg/log"
	"google.golang.org/protobuf/proto"
)

var helperLog log.Logger

func init() {
	helperLog = log.Get("helper")
}

// WrapConnectErrorDetail 给 connect 错误挂一条 detail。
// 构造失败只记日志并返回原错误 —— 这个函数在请求路径上，不该为附属信息 os.Exit。
func WrapConnectErrorDetail(connectErr *connect.Error, msg proto.Message) *connect.Error {
	errDetail, err := connect.NewErrorDetail(msg)
	if err != nil {
		helperLog.WithError(err).Error("build error detail failed")
		return connectErr
	}
	connectErr.AddDetail(errDetail)
	return connectErr
}
