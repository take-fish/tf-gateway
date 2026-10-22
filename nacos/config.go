package nacos

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/take-fish/tf-gateway/route"
)

const configRetryInterval = 5 * time.Second

// ConfigWatcher 负责首次加载 tf-gateway.yaml 并订阅后续变更。
type ConfigWatcher struct {
	settings Settings
	dataID   string
	group    string

	onSnapshot func(*route.Snapshot)
	onError    func(error)

	mu        sync.RWMutex
	state     string
	lastError string
	client    config_client.IConfigClient
}

// NewConfigWatcher 创建配置监听器，不会阻塞调用方。
func NewConfigWatcher(settings Settings, dataID, group string, onSnapshot func(*route.Snapshot), onError func(error)) *ConfigWatcher {
	return &ConfigWatcher{
		settings:   settings,
		dataID:     dataID,
		group:      group,
		onSnapshot: onSnapshot,
		onError:    onError,
		state:      "connecting",
	}
}

// Run 在后台运行，直到 ctx 取消。Nacos 不可用时按固定间隔重试。
func (w *ConfigWatcher) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if w.client == nil {
			client, err := NewConfigClient(w.settings)
			if err != nil {
				w.fail(err)
				if !sleep(ctx, configRetryInterval) {
					return
				}
				continue
			}
			w.client = client
		}

		content, err := w.client.GetConfig(vo.ConfigParam{DataId: w.dataID, Group: w.group})
		if err != nil || content == "" {
			if err == nil {
				err = fmt.Errorf("配置 %s/%s 为空", w.group, w.dataID)
			}
			w.fail(err)
			if !sleep(ctx, configRetryInterval) {
				return
			}
			continue
		}

		if err := w.apply(content); err != nil {
			w.fail(err)
			if !sleep(ctx, configRetryInterval) {
				return
			}
			continue
		}

		w.mu.Lock()
		w.state = "connected"
		w.lastError = ""
		w.mu.Unlock()

		param := vo.ConfigParam{
			DataId: w.dataID,
			Group:  w.group,
			OnChange: func(_, _, _, data string) {
				if err := w.apply(data); err != nil {
					w.fail(err)
					log.Printf("level=error component=nacos-config error=%q", err.Error())
					return
				}
				w.mu.Lock()
				w.state = "connected"
				w.lastError = ""
				w.mu.Unlock()
			},
		}
		if err := w.client.ListenConfig(param); err != nil {
			w.fail(err)
			if !sleep(ctx, configRetryInterval) {
				return
			}
			continue
		}
		// ListenConfig 启动内部轮询后即返回，后续重连由 SDK 负责。
		<-ctx.Done()
		return
	}
}

func (w *ConfigWatcher) apply(content string) error {
	snapshot, err := route.Parse([]byte(content))
	if err != nil {
		return fmt.Errorf("路由配置校验失败: %w", err)
	}
	if w.onSnapshot != nil {
		w.onSnapshot(snapshot)
	}
	log.Printf("level=info component=nacos-config route_version=%s routes=%d", snapshot.RouteVersion(), snapshot.Count())
	return nil
}

func (w *ConfigWatcher) fail(err error) {
	w.mu.Lock()
	w.state = "degraded"
	w.lastError = err.Error()
	w.mu.Unlock()
	if w.onError != nil {
		w.onError(err)
	}
}

// State 返回 connected/degraded/connecting。
func (w *ConfigWatcher) State() (string, string) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.state, w.lastError
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
