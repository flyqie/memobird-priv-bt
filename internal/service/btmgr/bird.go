package btmgr

import (
	"context"
	"errors"
	"github.com/flyqie/memobird-priv-bt/internal/model"
	"github.com/flyqie/memobird-priv-bt/internal/pkg/log"
	"github.com/flyqie/memobird-priv-bt/internal/service/birdbt"
	"github.com/muka/go-bluetooth/api"
	"github.com/muka/go-bluetooth/bluez/profile/adapter"
	"github.com/muka/go-bluetooth/bluez/profile/device"
	"github.com/muka/go-bluetooth/hw"
	"strings"
)

var btMgrLog log.Logger

func init() {
	btMgrLog = log.Get("btMgr")
}

type Bird struct {
	bird    *model.Bird
	ctx     context.Context
	cancel  context.CancelFunc
	a       *adapter.Adapter1
	birdBt  *birdbt.Bird
	btIface string
}

func NewBTMgr(bird *model.Bird, birdBt *birdbt.Bird, btIface string) *Bird {
	b := &Bird{
		bird:    bird,
		birdBt:  birdBt,
		btIface: btIface,
	}
	b.ctx, b.cancel = context.WithCancel(context.Background())
	return b
}

func (b *Bird) Start() error {
	var err error

	btmgmt := hw.NewBtMgmt(b.btIface)
	err = btmgmt.SetPowered(false)
	if err != nil {
		return err
	}
	err = btmgmt.SetLe(true)
	if err != nil {
		return err
	}
	err = btmgmt.SetBredr(true)
	if err != nil {
		return err
	}
	err = btmgmt.SetPowered(true)
	if err != nil {
		return err
	}

	b.a, err = adapter.NewAdapter1FromAdapterID(b.btIface)
	if err != nil {
		return err
	}
	for {
		select {
		case btMac := <-b.bird.BtMgrChan:
			btMgrLog.WithField("btMac", btMac).WithError(err).Debugf("Discover")
			err = b.discover(btMac)
			if err != nil {
				btMgrLog.WithField("btMac", btMac).WithError(err).Errorf("Discover failed")
			}
			b.bird.DeviceTempMapLock.Lock()
			delete(b.bird.DeviceTempMap, btMac)
			b.bird.DeviceTempMapLock.Unlock()
		case <-b.ctx.Done():
			return nil
		}
	}
}

func (b *Bird) Stop() error {
	b.cancel()
	return nil
}

func (b *Bird) discover(btMac string) error {
	err := b.a.FlushDevices()
	if err != nil {
		btMgrLog.WithField("btMac", btMac).Debugf("111111")
		return err
	}

	discovery, cancel, err := api.Discover(b.a, nil)
	if err != nil {
		btMgrLog.WithField("btMac", btMac).Debugf("222222")
		return err
	}
	defer cancel()

	for ev := range discovery {
		dev, err := device.NewDevice1(ev.Path)
		if err != nil {
			return err
		}
		if dev == nil || dev.Properties == nil {
			continue
		}

		p := dev.Properties
		if strings.ToLower(p.Address) != btMac {
			continue
		}

		go func(dev *device.Device1) {
			b.birdBt.HandleDevice(btMac, dev)
		}(dev)
		return nil
	}

	return errors.New("device not found")
}
