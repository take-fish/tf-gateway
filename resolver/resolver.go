package resolver

import (
	"errors"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
)

// ErrNoInstance 表示既没有健康实例，也没有可用静态回退地址。
var ErrNoInstance = errors.New("no healthy instance")

// Instance 是网关使用的实例视图，避免把 SDK 类型泄漏到代理层。
type Instance struct {
	IP       string
	Port     uint64
	Metadata map[string]string
}

// Discovery 提供按服务名查询的本地实例缓存。
// bool 表示该服务是否至少有过一次有效结果。
type Discovery interface {
	Instances(service string) ([]Instance, bool)
}

// Result 是一次解析结果。
type Result struct {
	Target   *url.URL
	Instance string
	Version  string
	Fallback bool
}

// Resolver 优先选择 Nacos 健康实例，其次按配置回退到静态地址。
type Resolver struct {
	discovery Discovery

	mu              sync.RWMutex
	static          map[string]*url.URL
	fallbackEnabled bool

	next uint64
}

// New 创建解析器。targets 的键为 Nacos 服务名，如 tf-user。
func New(discovery Discovery, targets map[string]*url.URL) *Resolver {
	return &Resolver{
		discovery:       discovery,
		static:          targets,
		fallbackEnabled: true,
	}
}

// SetFallbackEnabled 由路由快照驱动。
func (r *Resolver) SetFallbackEnabled(enabled bool) {
	r.mu.Lock()
	r.fallbackEnabled = enabled
	r.mu.Unlock()
}

// Resolve 按轮询选择健康实例；无实例时使用静态回退。
func (r *Resolver) Resolve(service string) (Result, error) {
	if r.discovery != nil {
		instances, _ := r.discovery.Instances(service)
		if len(instances) > 0 {
			idx := atomic.AddUint64(&r.next, 1) - 1
			inst := instances[idx%uint64(len(instances))]
			return Result{
				Target: &url.URL{
					Scheme: "http",
					Host:   hostPort(inst.IP, inst.Port),
				},
				Instance: hostPort(inst.IP, inst.Port),
				Version:  inst.Metadata["version"],
			}, nil
		}
	}

	r.mu.RLock()
	target := r.static[service]
	fallback := r.fallbackEnabled
	r.mu.RUnlock()
	if fallback && target != nil {
		return Result{
			Target:   target,
			Instance: target.Host,
			Fallback: true,
		}, nil
	}
	return Result{}, ErrNoInstance
}

// StaticTargets 把 TF_*_URL 转成服务名到 URL 的映射。
func StaticTargets(values map[string]string) map[string]*url.URL {
	targets := make(map[string]*url.URL, len(values))
	for service, raw := range values {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		targets[service] = u
	}
	return targets
}

func hostPort(ip string, port uint64) string {
	if port == 0 {
		return ip
	}
	return ip + ":" + strconv.FormatUint(port, 10)
}
