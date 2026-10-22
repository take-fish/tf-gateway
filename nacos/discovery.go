package nacos

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/take-fish/tf-gateway/resolver"
)

// Discovery 维护按服务名索引的健康实例缓存，并跟随路由快照调整订阅。
type Discovery struct {
	mu       sync.RWMutex
	client   naming_client.INamingClient
	group    string
	clusters []string
	refresh  time.Duration

	desired   []string
	instances map[string][]resolver.Instance
	subs      map[string]*vo.SubscribeParam
	state     string
	lastError string
}

// NewDiscovery 创建实例发现缓存。client 可为空，稍后通过 SetClient 注入。
func NewDiscovery(client naming_client.INamingClient, group string, clusters []string, refresh time.Duration) *Discovery {
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	if len(clusters) == 0 {
		clusters = []string{"DEFAULT"}
	}
	if refresh <= 0 {
		refresh = 10 * time.Second
	}
	return &Discovery{
		client:    client,
		group:     group,
		clusters:  append([]string(nil), clusters...),
		refresh:   refresh,
		instances: make(map[string][]resolver.Instance),
		subs:      make(map[string]*vo.SubscribeParam),
		state:     "connecting",
	}
}

// SetClient 注入命名客户端，并立即同步当前服务订阅。
func (d *Discovery) SetClient(client naming_client.INamingClient) {
	d.mu.Lock()
	d.client = client
	d.mu.Unlock()
	d.syncSubscriptions()
}

// UpdateConfig 应用 tf-gateway.yaml 中的发现配置。
// group/clusters 变化时重建订阅，避免继续使用旧范围。
func (d *Discovery) UpdateConfig(group string, clusters []string, refresh time.Duration) {
	d.mu.Lock()
	changed := (group != "" && group != d.group) ||
		(len(clusters) > 0 && !equalStrings(clusters, d.clusters))
	if group != "" {
		d.group = group
	}
	if len(clusters) > 0 {
		d.clusters = append([]string(nil), clusters...)
	}
	if refresh > 0 {
		d.refresh = refresh
	}
	client := d.client
	oldSubs := d.subs
	if changed {
		d.subs = make(map[string]*vo.SubscribeParam)
		d.instances = make(map[string][]resolver.Instance)
	}
	d.mu.Unlock()

	if changed && client != nil {
		for _, sub := range oldSubs {
			_ = client.Unsubscribe(sub)
		}
	}
	d.syncSubscriptions()
}

// UpdateServices 按快照中的服务集合增删订阅。
func (d *Discovery) UpdateServices(services []string) {
	d.mu.Lock()
	d.desired = append([]string(nil), services...)
	d.mu.Unlock()
	d.syncSubscriptions()
}

// Run 周期刷新实例，作为订阅回调之外的兜底。
func (d *Discovery) Run(ctx context.Context) {
	for {
		d.mu.RLock()
		interval := d.refresh
		d.mu.RUnlock()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			d.unsubscribeAll()
			return
		case <-timer.C:
			d.Refresh()
		}
	}
}

// Refresh 主动拉取所有已订阅服务的实例列表。
func (d *Discovery) Refresh() {
	d.mu.RLock()
	client := d.client
	group := d.group
	clusters := append([]string(nil), d.clusters...)
	services := append([]string(nil), d.desired...)
	d.mu.RUnlock()
	if client == nil {
		return
	}
	for _, service := range services {
		svc, err := client.GetService(vo.GetServiceParam{
			ServiceName: service,
			GroupName:   group,
			Clusters:    clusters,
		})
		if err != nil {
			d.markError(fmt.Errorf("刷新 %s 实例失败: %w", service, err))
			continue
		}
		d.store(service, svc.Hosts)
	}
}

// Instances 返回本地缓存；bool 表示该服务是否已有有效结果。
func (d *Discovery) Instances(service string) ([]resolver.Instance, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	list, ok := d.instances[service]
	return append([]resolver.Instance(nil), list...), ok
}

// State 返回 connected/degraded/connecting 与最近错误。
func (d *Discovery) State() (string, string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.state, d.lastError
}

func (d *Discovery) syncSubscriptions() {
	d.mu.RLock()
	client := d.client
	group := d.group
	clusters := append([]string(nil), d.clusters...)
	desired := append([]string(nil), d.desired...)
	d.mu.RUnlock()
	if client == nil {
		return
	}

	wanted := make(map[string]bool, len(desired))
	for _, service := range desired {
		wanted[service] = true
	}

	d.mu.Lock()
	for service, sub := range d.subs {
		if !wanted[service] {
			_ = client.Unsubscribe(sub)
			delete(d.subs, service)
			delete(d.instances, service)
		}
	}
	d.mu.Unlock()

	for _, service := range desired {
		d.mu.RLock()
		exists := d.subs[service] != nil
		d.mu.RUnlock()
		if exists {
			continue
		}
		service := service
		param := &vo.SubscribeParam{
			ServiceName: service,
			GroupName:   group,
			Clusters:    clusters,
			SubscribeCallback: func(instances []model.Instance, err error) {
				if err != nil {
					d.markError(fmt.Errorf("订阅 %s 回调失败: %w", service, err))
					return
				}
				d.store(service, instances)
			},
		}
		if err := client.Subscribe(param); err != nil {
			d.markError(fmt.Errorf("订阅 %s 失败: %w", service, err))
			continue
		}
		d.mu.Lock()
		d.subs[service] = param
		d.state = "connected"
		d.lastError = ""
		d.mu.Unlock()
	}
}

func (d *Discovery) store(service string, instances []model.Instance) {
	healthy := make([]resolver.Instance, 0, len(instances))
	for _, inst := range instances {
		if !inst.Healthy || !inst.Enable || inst.Weight <= 0 {
			continue
		}
		healthy = append(healthy, resolver.Instance{
			IP:       inst.Ip,
			Port:     inst.Port,
			Metadata: copyMetadata(inst.Metadata),
		})
	}
	d.mu.Lock()
	d.instances[service] = healthy
	d.state = "connected"
	d.lastError = ""
	d.mu.Unlock()
}

func (d *Discovery) markError(err error) {
	d.mu.Lock()
	d.state = "degraded"
	d.lastError = err.Error()
	d.mu.Unlock()
	slog.Warn("nacos discovery degraded", "event", "nacos.discovery", "component", "nacos-discovery", "error", err.Error())
}

func (d *Discovery) unsubscribeAll() {
	d.mu.Lock()
	client := d.client
	subs := d.subs
	d.subs = make(map[string]*vo.SubscribeParam)
	d.mu.Unlock()
	if client == nil {
		return
	}
	for _, sub := range subs {
		_ = client.Unsubscribe(sub)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func copyMetadata(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
