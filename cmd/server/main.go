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
	mainLog.Printf("Memobird Priv BT %s %s", gitTag, dateTime)
	if token := os.Getenv("ACCESS_TOKEN"); token != "" {
		interceptor.AccessToken = token
	} else {
		mainLog.Printf("Security Warning: ACCESS_TOKEN has not been set!")
	}
	if os.Getenv("MESSAGE_CB_CHECK") == "ENABLE" {
		mainLog.Printf("MESSAGE_CB_CHECK ENABLE!")
	}
	btIface := "hci0"
	if os.Getenv("BT_IFACE") != "" {
		btIface = os.Getenv("BT_IFACE")
	}
	mainLog.Printf("Bluetooth Interface: %s", btIface)
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
	mainLog.Printf("mgrApi server listening on 0.0.0.0:8082")
	go func() {
		if err := mgrApiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			cancel()
			errExit = true
			time.Sleep(time.Second * 2)
			mainLog.WithError(err).Errorf("mgrApi listen and serve")
		}
		wg.Done()
	}()

	birdBt := birdbt.NewBT(bird, btIface)
	btMgr := btmgr.NewBTMgr(bird, birdBt, btIface)
	go func() {
		mainLog.Printf("BluetoothMgr listening")
		if err := btMgr.Start(); err != nil {
			cancel()
			errExit = true
			time.Sleep(time.Second * 2)
			mainLog.WithError(err).Errorf("BluetoothMgr listen and serve")
		}
		wg.Done()
	}()

	<-ctx.Done()
	wg.Add(1)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	if err := mgrApiSrv.Shutdown(ctx2); err != nil {
		mainLog.WithError(err).Fatalf("mgrApiSrv shutdown")
	}
	wg.Add(1)
	if err := btMgr.Stop(); err != nil {
		mainLog.WithError(err).Fatalf("DNS shutdown")
	}
	wg.Wait()
	if errExit {
		os.Exit(1)
	}
}
