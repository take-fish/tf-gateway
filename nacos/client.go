package nacos

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// Settings 是网关连接 Nacos 所需的全部参数。
type Settings struct {
	Addr      string
	Namespace string
	Username  string
	Password  string
	LogDir    string
	CacheDir  string
	Timeout   time.Duration
}

// NewConfigClient 创建配置客户端。失败不代表网关不可用，调用方可重试。
func NewConfigClient(s Settings) (config_client.IConfigClient, error) {
	param, err := buildParam(s)
	if err != nil {
		return nil, err
	}
	client, err := clients.NewConfigClient(param)
	if err != nil {
		return nil, fmt.Errorf("创建 Nacos 配置客户端失败: %w", err)
	}
	return client, nil
}

// NewNamingClient 创建命名服务客户端，用于订阅实例。
func NewNamingClient(s Settings) (naming_client.INamingClient, error) {
	param, err := buildParam(s)
	if err != nil {
		return nil, err
	}
	client, err := clients.NewNamingClient(param)
	if err != nil {
		return nil, fmt.Errorf("创建 Nacos 命名客户端失败: %w", err)
	}
	return client, nil
}

func buildParam(s Settings) (vo.NacosClientParam, error) {
	host, port, scheme, err := parseAddr(s.Addr)
	if err != nil {
		return vo.NacosClientParam{}, err
	}
	timeout := uint64(5000)
	if s.Timeout > 0 {
		timeout = uint64(s.Timeout / time.Millisecond)
	}
	clientConfig := constant.ClientConfig{
		NamespaceId:         s.Namespace,
		Username:            s.Username,
		Password:            s.Password,
		TimeoutMs:           timeout,
		NotLoadCacheAtStart: true,
		AppendToStdout:      true,
		LogLevel:            "warn",
	}
	if s.LogDir != "" {
		clientConfig.LogDir = s.LogDir
	}
	if s.CacheDir != "" {
		clientConfig.CacheDir = s.CacheDir
	}
	serverConfig := constant.ServerConfig{
		IpAddr:      host,
		Port:        port,
		Scheme:      scheme,
		ContextPath: "/nacos",
	}
	return vo.NacosClientParam{
		ClientConfig:  &clientConfig,
		ServerConfigs: []constant.ServerConfig{serverConfig},
	}, nil
}

func parseAddr(addr string) (string, uint64, string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0, "", fmt.Errorf("NACOS_ADDR 不能为空")
	}
	scheme := "http"
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", 0, "", fmt.Errorf("解析 NACOS_ADDR 失败: %w", err)
		}
		scheme = u.Scheme
		addr = u.Host
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		portText = "8848"
	}
	var port uint64
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil || port == 0 {
		return "", 0, "", fmt.Errorf("NACOS_ADDR 端口非法: %s", addr)
	}
	if host == "" {
		return "", 0, "", fmt.Errorf("NACOS_ADDR 主机为空")
	}
	return host, port, scheme, nil
}
