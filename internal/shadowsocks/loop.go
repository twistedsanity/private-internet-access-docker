package shadowsocks

import (
	"context"
	"sync"
	"time"

	"github.com/qdm12/golibs/command"
	"github.com/qdm12/golibs/logging"
	"github.com/qdm12/private-internet-access-docker/internal/firewall"
	"github.com/qdm12/private-internet-access-docker/internal/settings"
)

type Looper interface {
	Run(ctx context.Context, wg *sync.WaitGroup)
	Restart()
	Start()
	Stop()
	GetSettings() (settings settings.ShadowSocks)
	SetSettings(settings settings.ShadowSocks)
}

type looper struct {
	conf          Configurator
	firewallConf  firewall.Configurator
	settings      settings.ShadowSocks
	settingsMutex sync.RWMutex
	dnsSettings   settings.DNS // TODO
	logger        logging.Logger
	streamMerger  command.StreamMerger
	uid           int
	gid           int
	restart       chan struct{}
	start         chan struct{}
	stop          chan struct{}
}

func (l *looper) logAndWait(ctx context.Context, err error) {
	l.logger.Error(err)
	l.logger.Info("retrying in 1 minute")
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel() // just for the linter
	<-ctx.Done()
}

func NewLooper(conf Configurator, firewallConf firewall.Configurator, settings settings.ShadowSocks, dnsSettings settings.DNS,
	logger logging.Logger, streamMerger command.StreamMerger, uid, gid int) Looper {
	return &looper{
		conf:         conf,
		firewallConf: firewallConf,
		settings:     settings,
		dnsSettings:  dnsSettings,
		logger:       logger.WithPrefix("shadowsocks: "),
		streamMerger: streamMerger,
		uid:          uid,
		gid:          gid,
		restart:      make(chan struct{}),
		start:        make(chan struct{}),
		stop:         make(chan struct{}),
	}
}

func (l *looper) Restart() { l.restart <- struct{}{} }
func (l *looper) Start()   { l.start <- struct{}{} }
func (l *looper) Stop()    { l.stop <- struct{}{} }

func (l *looper) GetSettings() (settings settings.ShadowSocks) {
	l.settingsMutex.RLock()
	defer l.settingsMutex.RUnlock()
	return l.settings
}

func (l *looper) SetSettings(settings settings.ShadowSocks) {
	l.settingsMutex.Lock()
	defer l.settingsMutex.Unlock()
	l.settings = settings
}

func (l *looper) isEnabled() bool {
	l.settingsMutex.RLock()
	defer l.settingsMutex.RUnlock()
	return l.settings.Enabled
}

func (l *looper) setEnabled(enabled bool) {
	l.settingsMutex.Lock()
	defer l.settingsMutex.Unlock()
	l.settings.Enabled = enabled
}

func (l *looper) Run(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	defer wg.Done()
	waitForStart := true
	for waitForStart {
		select {
		case <-l.stop:
			l.logger.Info("not started yet")
		case <-l.start:
			waitForStart = false
		case <-l.restart:
			waitForStart = false
		case <-ctx.Done():
			return
		}
	}
	defer l.logger.Warn("loop exited")

	l.setEnabled(true)

	var previousPort uint16
	for ctx.Err() == nil {
		for !l.isEnabled() {
			// wait for a signal to re-enable
			select {
			case <-l.stop:
				l.logger.Info("already disabled")
			case <-l.restart:
				l.setEnabled(true)
			case <-l.start:
				l.setEnabled(true)
			case <-ctx.Done():
				return
			}
		}

		nameserver := l.dnsSettings.PlaintextAddress.String()
		if l.dnsSettings.Enabled {
			nameserver = "127.0.0.1"
		}
		settings := l.GetSettings()
		err := l.conf.MakeConf(settings.Port, settings.Password, settings.Method, nameserver, l.uid, l.gid)
		if err != nil {
			l.logAndWait(ctx, err)
			continue
		}

		if previousPort > 0 {
			if err := l.firewallConf.RemoveAllowedPort(ctx, previousPort); err != nil {
				l.logger.Error(err)
				continue
			}
		}
		if err := l.firewallConf.SetAllowedPort(ctx, settings.Port); err != nil {
			l.logger.Error(err)
			continue
		}
		previousPort = settings.Port

		shadowsocksCtx, shadowsocksCancel := context.WithCancel(context.Background())
		stdout, stderr, waitFn, err := l.conf.Start(shadowsocksCtx, "0.0.0.0", settings.Port, settings.Password, settings.Log)
		if err != nil {
			shadowsocksCancel()
			l.logAndWait(ctx, err)
			continue
		}
		go l.streamMerger.Merge(shadowsocksCtx, stdout, command.MergeName("shadowsocks"))
		go l.streamMerger.Merge(shadowsocksCtx, stderr, command.MergeName("shadowsocks error"))
		waitError := make(chan error)
		go func() {
			err := waitFn() // blocking
			waitError <- err
		}()

		stayHere := true
		for stayHere {
			select {
			case <-ctx.Done():
				l.logger.Warn("context canceled: exiting loop")
				shadowsocksCancel()
				<-waitError
				close(waitError)
				return
			case <-l.restart: // triggered restart
				l.logger.Info("restarting")
				shadowsocksCancel()
				<-waitError
				close(waitError)
				stayHere = false
			case <-l.start:
				l.logger.Info("already started")
			case <-l.stop:
				l.logger.Info("stopping")
				shadowsocksCancel()
				<-waitError
				close(waitError)
				l.setEnabled(false)
				stayHere = false
			case err := <-waitError: // unexpected error
				shadowsocksCancel()
				close(waitError)
				l.logAndWait(ctx, err)
			}
		}
		shadowsocksCancel() // repetition for linter only
	}
}
