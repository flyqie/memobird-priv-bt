package main

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	v1Connect "github.com/flyqie/memobird-priv/gen/memobird/v1/v1connect"
	"github.com/flyqie/memobird-priv/internal/interceptor"
	"github.com/flyqie/memobird-priv/internal/model"
	"github.com/flyqie/memobird-priv/internal/pkg/log"
	"github.com/flyqie/memobird-priv/internal/service/birdhttp"
	"github.com/flyqie/memobird-priv/internal/service/birdws"
	"github.com/flyqie/memobird-priv/internal/service/dns"
	apiServ "github.com/flyqie/memobird-priv/internal/service/mgr"
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
	mainLog.Printf("Memobird Priv %s %s", gitTag, dateTime)
	if token := os.Getenv("ACCESS_TOKEN"); token != "" {
		interceptor.AccessToken = token
	} else {
		mainLog.Printf("Security Warning: ACCESS_TOKEN has not been set!")
	}
	var ip string
	if ip = os.Getenv("PRIV_SERVER_IP"); ip == "" {
		mainLog.Fatalf("PRIV_SERVER_IP has not been set!")
	}
	if os.Getenv("MESSAGE_CB_CHECK") == "ENABLE" {
		mainLog.Printf("MESSAGE_CB_CHECK ENABLE!")
	}
	bird := &model.Bird{
		DeviceMap: make(map[string]*model.BirdDevice),
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

	httpServerMux := http.NewServeMux()
	birdHttp := birdhttp.NewHTTP(ip)
	birdHttp.Bind(httpServerMux)
	mgrApi := http.NewServeMux()
	mgrApi.Handle(v1Connect.NewApiServiceHandler(
		&apiServ.ApiServer{
			Bird: bird,
		},
		connect.WithInterceptors(interceptor.NewAuthInterceptor()),
	))
	httpServerMux.Handle("/mgr_api/", http.StripPrefix("/mgr_api", mgrApi))
	httpSrv := &http.Server{
		Addr:    "0.0.0.0:80",
		Handler: h2c.NewHandler(httpServerMux, &http2.Server{}),
	}
	mainLog.Printf("HTTP server listening on 0.0.0.0:80")
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			cancel()
			errExit = true
			time.Sleep(time.Second * 2)
			mainLog.WithError(err).Errorf("HTTP listen and serve")
		}
		wg.Done()
	}()

	birdDns := dns.NewDNS(ip)
	go func() {
		mainLog.Printf("DNS server listening on 0.0.0.0:53")
		if err := birdDns.Start(); err != nil {
			cancel()
			errExit = true
			time.Sleep(time.Second * 2)
			mainLog.WithError(err).Errorf("DNS listen and serve")
		}
		wg.Done()
	}()

	wsServerMux := http.NewServeMux()
	birdWS := birdws.NewWS(bird)
	birdWS.Bind(wsServerMux)
	wsSrv := &http.Server{
		Addr:    "0.0.0.0:81",
		Handler: h2c.NewHandler(wsServerMux, &http2.Server{}),
	}
	mainLog.Printf("WEBSOCKET server listening on 0.0.0.0:81")
	go func() {
		if err := wsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			cancel()
			errExit = true
			time.Sleep(time.Second * 2)
			mainLog.WithError(err).Errorf("WEBSOCKET listen and serve")
		}
		wg.Done()
	}()

	<-ctx.Done()
	wg.Add(1)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel2()
	if err := httpSrv.Shutdown(ctx2); err != nil {
		mainLog.WithError(err).Fatalf("HTTP shutdown")
	}
	wg.Add(1)
	if err := birdDns.Stop(); err != nil {
		mainLog.WithError(err).Fatalf("DNS shutdown")
	}
	wg.Add(1)
	ctx3, cancel3 := context.WithTimeout(context.Background(), time.Second)
	defer cancel3()
	if err := wsSrv.Shutdown(ctx3); err != nil {
		mainLog.WithError(err).Fatalf("WEBSOCKET shutdown")
	}
	wg.Wait()
	if errExit {
		os.Exit(1)
	}
}
