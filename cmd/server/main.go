package main

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	v1Connect "github.com/flyqie/memobird-priv-bt/gen/memobird/v1/v1connect"
	"github.com/flyqie/memobird-priv-bt/internal/interceptor"
	"github.com/flyqie/memobird-priv-bt/internal/model"
	"github.com/flyqie/memobird-priv-bt/internal/pkg/log"
	"github.com/flyqie/memobird-priv-bt/internal/service/birdbt"
	"github.com/flyqie/memobird-priv-bt/internal/service/btmgr"
	apiServ "github.com/flyqie/memobird-priv-bt/internal/service/mgr"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

var gitTag string
var dateTime string

var mainLog log.Logger

func init() {
	mainLog = log.Get("main")
}

func main() {
	mainLog.Infof("memobird-priv-bt %s %s", gitTag, dateTime)
	if token := os.Getenv("ACCESS_TOKEN"); token != "" {
		interceptor.AccessToken = token
	} else {
		mainLog.Warn("ACCESS_TOKEN is not set, API authentication is disabled")
	}
	if os.Getenv("MESSAGE_CB_CHECK") == "ENABLE" {
		mainLog.Info("paper message callback check enabled")
	}
	if v := os.Getenv("BT_PACKET_INTERVAL_MS"); v != "" {
		mainLog.WithField("value", v).Info("BT_PACKET_INTERVAL_MS is set")
	}
	btIface := "hci0"
	if os.Getenv("BT_IFACE") != "" {
		btIface = os.Getenv("BT_IFACE")
	}
	mainLog.WithField("btIface", btIface).Info("bluetooth interface")
	bird := &model.Bird{
		DeviceMap:     make(map[string]*model.BirdDevice),
		DeviceTempMap: make(map[string]time.Time),
		BtMgrChan:     make(chan string, 10000),
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-signals
		cancel()
	}()
	wg := &sync.WaitGroup{}
	var errExit bool

	mgrApi := http.NewServeMux()
	mgrApi.Handle(v1Connect.NewApiServiceHandler(
		&apiServ.ApiServer{
			Bird: bird,
		},
		connect.WithInterceptors(interceptor.NewAuthInterceptor()),
	))
	mgrApiSrv := &http.Server{
		Addr:    "0.0.0.0:8082",
		Handler: h2c.NewHandler(mgrApi, &http2.Server{}),
	}
	mainLog.Info("api server listening on 0.0.0.0:8082")
	// Add 必须在启动 goroutine 之前，否则 Done() 先跑会把计数变成负数并 panic
	wg.Add(2)
	go func() {
		if err := mgrApiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			cancel()
			errExit = true
			time.Sleep(time.Second * 2)
			mainLog.WithError(err).Error("api server stopped")
		}
		wg.Done()
	}()

	birdBt := birdbt.NewBT(bird, btIface)
	btMgr := btmgr.NewBTMgr(bird, birdBt, btIface)
	go func() {
		mainLog.Info("bluetooth manager started")
		if err := btMgr.Start(); err != nil {
			cancel()
			errExit = true
			time.Sleep(time.Second * 2)
			mainLog.WithError(err).Error("bluetooth manager stopped")
		}
		wg.Done()
	}()

	<-ctx.Done()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	// 用 Error 而不是 Fatal：Fatal 会直接 os.Exit，跳过下面的 wg.Wait()
	if err := mgrApiSrv.Shutdown(ctx2); err != nil {
		mainLog.WithError(err).Error("api server shutdown failed")
	}
	if err := btMgr.Stop(); err != nil {
		mainLog.WithError(err).Error("bluetooth manager shutdown failed")
	}
	wg.Wait()
	if errExit {
		os.Exit(1)
	}
}
