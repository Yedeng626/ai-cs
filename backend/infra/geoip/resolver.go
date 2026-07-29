package geoip

import (
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/service"
)

// Resolver 根据 IP 解析大致地理位置（离线）。
type Resolver interface {
	Lookup(ip string) string
	Close()
}

// NoopResolver 未配置 xdb 时使用。
type NoopResolver struct{}

func (NoopResolver) Lookup(string) string { return "" }
func (NoopResolver) Close()               {}

type ip2regionResolver struct {
	svc *service.Ip2Region
}

func (r *ip2regionResolver) Lookup(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	if parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsLinkLocalUnicast() {
		return "内网"
	}
	region, err := r.svc.Search(ip)
	if err != nil || strings.TrimSpace(region) == "" {
		return ""
	}
	return FormatRegion(region)
}

func (r *ip2regionResolver) Close() {
	if r.svc != nil {
		r.svc.Close()
	}
}

var (
	globalResolver Resolver = NoopResolver{}
	globalMu       sync.Mutex
)

// Get 返回全局 Resolver（未初始化时为 Noop）。
func Get() Resolver {
	globalMu.Lock()
	defer globalMu.Unlock()
	return globalResolver
}

// InitFromEnv 按环境变量与默认路径加载 ip2region；失败则降级为 Noop，不阻塞启动。
// IP2REGION_DISABLED=true 时跳过；IP2REGION_V4_XDB 指定 v4 库路径；IP2REGION_V6_XDB 可选 IPv6。
func InitFromEnv() {
	globalMu.Lock()
	defer globalMu.Unlock()

	if old, ok := globalResolver.(*ip2regionResolver); ok {
		old.Close()
	}
	globalResolver = NoopResolver{}

	if strings.EqualFold(strings.TrimSpace(os.Getenv("IP2REGION_DISABLED")), "true") {
		log.Println("ℹ️ IP2REGION_DISABLED=true，跳过访客 IP 地理位置解析")
		return
	}

	v4Path := strings.TrimSpace(os.Getenv("IP2REGION_V4_XDB"))
	v6Path := strings.TrimSpace(os.Getenv("IP2REGION_V6_XDB"))
	if v4Path == "" {
		v4Path = findDefaultXDB("ip2region_v4.xdb")
	}
	if v6Path == "" {
		v6Path = findDefaultXDB("ip2region_v6.xdb")
	}
	if v4Path == "" && v6Path == "" {
		log.Println("⚠️ 未找到 ip2region xdb，访客「位置」将保持为空；请放置 backend/data/ip2region_v4.xdb（及可选 v6）或设置 IP2REGION_V4_XDB / IP2REGION_V6_XDB")
		return
	}

	svc, err := service.NewIp2RegionWithPath(v4Path, v6Path)
	if err != nil {
		log.Printf("⚠️ 初始化 ip2region 失败: %v", err)
		return
	}
	globalResolver = &ip2regionResolver{svc: svc}
	if v4Path != "" {
		log.Printf("✅ ip2region 已加载 (v4: %s)", v4Path)
	}
	if v6Path != "" {
		log.Printf("✅ ip2region 已加载 (v6: %s)", v6Path)
	} else {
		log.Println("ℹ️ 未加载 ip2region IPv6 库；访客若为 IPv6 地址将无法解析位置，可下载 ip2region_v6.xdb")
	}
}

// LookupLocation 解析 IP；已有 location 非空时原样返回。
func LookupLocation(ip, currentLocation string) string {
	if strings.TrimSpace(currentLocation) != "" {
		return currentLocation
	}
	return Get().Lookup(ip)
}

func findDefaultXDB(filename string) string {
	candidates := []string{
		"data/" + filename,
		"backend/data/" + filename,
		"/app/data/" + filename,
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "data", filename),
			filepath.Join(wd, "..", "backend", "data", filename),
		)
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			abs, err := filepath.Abs(p)
			if err == nil {
				return abs
			}
			return p
		}
	}
	return ""
}
