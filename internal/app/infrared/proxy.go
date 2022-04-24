package infrared

import (
	"github.com/haveachin/infrared/pkg/event"
	"go.uber.org/zap"
)

type ProxyConfig interface {
	LoadGateways() ([]Gateway, error)
	LoadServers() ([]Server, error)
	LoadConnProcessor() (ConnProcessor, error)
	LoadProxySettings() (ProxySettings, error)
}

type ProxyChannelCaps struct {
	ConnProcessor int
	Server        int
	ConnPool      int
}

type ProxySettings struct {
	ChannelCaps ProxyChannelCaps
	CPNCount    int
}

type Proxy struct {
	settings      ProxySettings
	gateways      []Gateway
	cpnPool       CPNPool
	serverGateway ServerGateway
	connPool      ConnPool
	cpnCh         chan Conn
	srvCh         chan ProcessedConn
	poolCh        chan ConnTunnel
	logger        *zap.Logger
}

func NewProxy(cfg ProxyConfig) (Proxy, error) {
	gws, err := cfg.LoadGateways()
	if err != nil {
		return Proxy{}, err
	}

	cp, err := cfg.LoadConnProcessor()
	if err != nil {
		return Proxy{}, err
	}

	srvs, err := cfg.LoadServers()
	if err != nil {
		return Proxy{}, err
	}

	stg, err := cfg.LoadProxySettings()
	if err != nil {
		return Proxy{}, err
	}

	chCaps := stg.ChannelCaps
	cpnCh := make(chan Conn, chCaps.ConnProcessor)
	srvCh := make(chan ProcessedConn, chCaps.Server)
	poolCh := make(chan ConnTunnel, chCaps.ConnPool)

	return Proxy{
		settings: stg,
		gateways: gws,
		cpnPool: CPNPool{
			CPN: CPN{
				ConnProcessor: cp,
				In:            cpnCh,
				Out:           srvCh,
			},
		},
		serverGateway: ServerGateway{
			ServerGatewayConfig: ServerGatewayConfig{
				Gateways: gws,
				Servers:  srvs,
				In:       srvCh,
				Out:      poolCh,
			},
		},
		connPool: ConnPool{
			ConnPoolConfig: ConnPoolConfig{
				In: poolCh,
			},
		},
		cpnCh:  cpnCh,
		srvCh:  srvCh,
		poolCh: poolCh,
	}, nil
}

func (p *Proxy) ListenAndServe(logger *zap.Logger) {
	p.logger = logger
	p.cpnPool.CPN.Logger = logger
	p.cpnPool.CPN.EventBus = event.DefaultBus
	p.cpnPool.SetSize(p.settings.CPNCount)

	for _, gw := range p.gateways {
		gw.SetLogger(logger)
		go ListenAndServe(gw, p.cpnCh)
	}

	p.connPool.Logger = logger
	go p.connPool.Start()

	p.serverGateway.Logger = logger
	p.serverGateway.Start()
}

func (p *Proxy) Reload(cfg ProxyConfig) error {
	np, err := NewProxy(cfg)
	if err != nil {
		return err
	}
	np.cpnPool.CPN.EventBus = event.DefaultBus
	np.cpnPool.CPN.Logger = p.logger
	np.serverGateway.Logger = p.logger
	np.connPool.ConnPoolConfig.Logger = p.logger

	for _, gw := range p.gateways {
		gw.Close()
	}

	p.gateways = np.gateways
	p.settings = np.settings
	p.cpnPool.SetSize(0)
	p.cpnPool.CPN = np.cpnPool.CPN
	p.cpnPool.SetSize(p.settings.CPNCount)
	p.serverGateway.Reload(np.serverGateway.ServerGatewayConfig)
	p.connPool.Reload(np.connPool.ConnPoolConfig)

	close(p.cpnCh)
	for c := range p.cpnCh {
		np.cpnCh <- c
	}
	p.cpnCh = np.cpnCh

	close(p.srvCh)
	for c := range p.srvCh {
		np.srvCh <- c
	}
	p.srvCh = np.srvCh

	close(p.poolCh)
	for c := range p.poolCh {
		np.poolCh <- c
	}
	p.poolCh = np.poolCh

	for _, gw := range p.gateways {
		gw.SetLogger(p.logger)
		go ListenAndServe(gw, p.cpnCh)
	}
	return nil
}

func (p *Proxy) Close() {
	for _, gw := range p.gateways {
		gw.Close()
	}
	p.serverGateway.Close()
	p.cpnPool.Close()
	close(p.cpnCh)
	close(p.srvCh)
	close(p.poolCh)
}