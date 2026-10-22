package main

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/take-fish/tf-gateway/nacos"
	"github.com/take-fish/tf-gateway/resolver"
	"github.com/take-fish/tf-gateway/route"
)

// App 持有不可变路由快照、实例解析器与代理。
// HTTP 服务先启动，Nacos 在后台连接，不阻塞启动。
type App struct {
	cfg Config

	snapshot atomic.Pointer[route.Snapshot]
	resolver *resolver.Resolver
	proxy    *Proxy

	discovery *nacos.Discovery
	watcher   *nacos.ConfigWatcher

	configState    atomicString
	configError    atomicString
	discoveryState atomicString
	discoveryError atomicString
}

// NewApp 使用内置默认路由立即构造可用网关。
func NewApp(cfg Config) *App {
	snapshot := route.Default()
	if !cfg.DiscoveryEnabled {
		disabled := false
		snapshot.Gateway.Discovery.Enabled = &disabled
	}

	app := &App{cfg: cfg}
	app.snapshot.Store(snapshot)
	app.configState.Set("disabled")
	app.discoveryState.Set("disabled")

	var discovery *nacos.Discovery
	var discoveryProvider resolver.Discovery
	if cfg.DiscoveryEnabled {
		app.configState.Set("connecting")
		app.discoveryState.Set("connecting")
		discovery = nacos.NewDiscovery(nil, cfg.DiscoveryGroup, []string{"DEFAULT"}, 10*time.Second)
		discoveryProvider = discovery
	}
	app.discovery = discovery
	app.resolver = resolver.New(discoveryProvider, resolver.StaticTargets(cfg.Static))
	app.resolver.SetFallbackEnabled(snapshot.FallbackEnabled())
	app.proxy = NewProxy(app)
	return app
}

// Start 在后台连接 Nacos Config 与 Discovery。
func (a *App) Start(ctx context.Context) {
	if !a.cfg.DiscoveryEnabled {
		log.Printf("level=info component=nacos discovery=disabled fallback=static")
		return
	}
	settings := nacos.Settings{
		Addr:      a.cfg.NacosAddr,
		Namespace: a.cfg.NacosNamespace,
		Username:  a.cfg.NacosUsername,
		Password:  a.cfg.NacosPassword,
		LogDir:    a.cfg.NacosLogDir,
		CacheDir:  a.cfg.NacosCacheDir,
		Timeout:   5 * time.Second,
	}
	a.watcher = nacos.NewConfigWatcher(
		settings,
		a.cfg.ConfigDataID,
		a.cfg.ConfigGroup,
		a.SetSnapshot,
		a.setConfigError,
	)
	go a.watcher.Run(ctx)
	go a.runDiscovery(ctx, settings)
}

func (a *App) runDiscovery(ctx context.Context, settings nacos.Settings) {
	for ctx.Err() == nil {
		client, err := nacos.NewNamingClient(settings)
		if err == nil {
			a.discovery.SetClient(client)
			a.discovery.Run(ctx)
			return
		}
		a.discoveryError.Set(err.Error())
		log.Printf("level=warn component=nacos-discovery error=%q retry_in=5s", err.Error())
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// SetSnapshot 原子替换路由，并按新配置调整订阅与代理超时。
func (a *App) SetSnapshot(snapshot *route.Snapshot) {
	if snapshot == nil {
		return
	}
	a.snapshot.Store(snapshot)
	a.resolver.SetFallbackEnabled(snapshot.FallbackEnabled())
	if a.discovery != nil {
		a.discovery.UpdateConfig(
			snapshot.Gateway.Discovery.Group,
			snapshot.Gateway.Discovery.Clusters,
			time.Duration(snapshot.Gateway.Discovery.RefreshIntervalSecond)*time.Second,
		)
		a.discovery.UpdateServices(snapshot.Services())
	}
	a.proxy.UpdateTransport(snapshot.HTTP())
	a.configState.Set("connected")
	a.configError.Set("")
	log.Printf("level=info component=gateway route_version=%s routes=%d", snapshot.RouteVersion(), snapshot.Count())
}

func (a *App) setConfigError(err error) {
	if err == nil {
		a.configError.Set("")
		return
	}
	a.configState.Set("degraded")
	a.configError.Set(err.Error())
	log.Printf("level=warn component=nacos-config error=%q fallback=last_snapshot", err.Error())
}

// Snapshot 返回当前路由快照。
func (a *App) Snapshot() *route.Snapshot {
	return a.snapshot.Load()
}

// Ready 表示网关至少有一份可用路由。
func (a *App) Ready() bool {
	snapshot := a.Snapshot()
	return snapshot != nil && snapshot.Count() > 0
}

// Health 汇总 Nacos 与路由状态。
func (a *App) Health() Health {
	snapshot := a.Snapshot()
	configState := a.configState.Get()
	configErr := a.configError.Get()
	discoveryState := a.discoveryState.Get()
	discoveryErr := a.discoveryError.Get()

	if a.cfg.DiscoveryEnabled && a.watcher != nil {
		state, errText := a.watcher.State()
		configState = state
		if errText != "" {
			configErr = errText
		}
	}
	if a.cfg.DiscoveryEnabled && a.discovery != nil {
		state, errText := a.discovery.State()
		discoveryState = state
		if errText != "" {
			discoveryErr = errText
		}
	}

	status := "ok"
	if snapshot == nil || snapshot.Count() == 0 {
		status = "failed"
	} else if !a.cfg.DiscoveryEnabled || configState != "connected" || discoveryState != "connected" {
		status = "degraded"
	}

	return Health{
		Status:  status,
		Service: "tf-gateway",
		Version: version,
		Nacos: NacosHealth{
			Config:    configState,
			Discovery: discoveryState,
			Error:     firstNonEmpty(configErr, discoveryErr),
		},
		RouteVersion: snapshot.RouteVersion(),
		Routes:       snapshot.Count(),
	}
}

type Health struct {
	Status       string      `json:"status"`
	Service      string      `json:"service"`
	Version      string      `json:"version"`
	Nacos        NacosHealth `json:"nacos"`
	RouteVersion string      `json:"routeVersion"`
	Routes       int         `json:"routes"`
}

type NacosHealth struct {
	Config    string `json:"config"`
	Discovery string `json:"discovery"`
	Error     string `json:"error,omitempty"`
}

type atomicString struct {
	value atomic.Value
}

func (a *atomicString) Set(value string) {
	a.value.Store(value)
}

func (a *atomicString) Get() string {
	if value, ok := a.value.Load().(string); ok {
		return value
	}
	return ""
}
