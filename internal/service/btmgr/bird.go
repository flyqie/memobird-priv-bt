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
)

var btMgrLog log.Logger

func init() {
	btMgrLog = log.Get("btMgr")
}

type Bird struct {
	bird   *model.Bird
	ctx    context.Context
	cancel context.CancelFunc
	a      *adapter.Adapter1
	birdBt *birdbt.Bird
}

func NewBTMgr(bird *model.Bird, birdBt *birdbt.Bird) *Bird {
	b := &Bird{
		bird:   bird,
		birdBt: birdBt,
	}
	b.ctx, b.cancel = context.WithCancel(context.Background())
	return b
}

func (b *Bird) Start() error {
	var err error
	b.a, err = adapter.NewAdapter1FromAdapterID("hci0")
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
		return err
	}

	discovery, cancel, err := api.Discover(b.a, nil)
	if err != nil {
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
		if p.Address != btMac {
			continue
		}

		go func(dev *device.Device1) {
			b.birdBt.HandleDevice("hci0", dev)
		}(dev)
		return nil
	}

	return errors.New("device not found")
}
