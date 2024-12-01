package helper

import (
	"connectrpc.com/connect"
	"github.com/flyqie/memobird-priv/internal/pkg/log"
	"google.golang.org/protobuf/proto"
)

var helperLog log.Logger

func init() {
	helperLog = log.Get("helper")
}

func WrapConnectErrorDetail(connectErr *connect.Error, msg proto.Message) *connect.Error {
	errDetail, err := connect.NewErrorDetail(msg)
	if err != nil {
		helperLog.WithError(err).Fatalf("connect.NewErrorDetail")
	}
	connectErr.AddDetail(errDetail)
	return connectErr
}
