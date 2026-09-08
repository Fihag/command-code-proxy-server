package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dev2k6/command-code-proxy-server/internal/proxy"
	"github.com/dev2k6/command-code-proxy-server/internal/server"
	"github.com/dev2k6/command-code-proxy-server/internal/update"
)

const appVersion = "v1.0.8"
const repositoryURL = "https://github.com/dev2k6/command-code-proxy-server"
const debugLogging = false

func main() {
	port := flag.String("port", "", "监听端口 (默认: 55990)")
	host := flag.String("host", "", "绑定地址 (默认: 127.0.0.1)")
	apiKeyFlag := flag.String("api-key", "", "CommandCode API 密钥 (可选, 也可通过 COMMANDCODE_API_KEY 环境变量或 Authorization 请求头传入)")
	projectSlug := flag.String("project-slug", "", "上报给上游的项目名 x-project-slug (默认: 由工作目录推导)")
	workDir := flag.String("workdir", "", "上报给上游 config 快照的工作目录 (默认: COMMANDCODE_WORKING_DIR 或用户主目录; 切勿用本代理仓库目录启动)")
	showVersion := flag.Bool("version", false, "打印版本号后退出")
	flag.Parse()

	// 命令行明文密钥会出现在进程列表/命令历史里；环境变量优先兜底。
	key := *apiKeyFlag
	if key == "" {
		key = os.Getenv("COMMANDCODE_API_KEY")
	}

	if *showVersion {
		fmt.Println(versionText())
		return
	}

	proxy := proxy.NewProxy(key)
	if *workDir != "" {
		proxy.SetWorkingDir(*workDir) // config 快照与 x-project-slug 同源
	}
	if *projectSlug != "" {
		proxy.SetProjectSlug(*projectSlug)
	}
	proxy.Debug = debugLogging
	// StartBeacon 之后再不得替换 identity/envCfg：runBeacon 无锁读取
	// processSess，setter 整体替换指针，先改完再信标。
	proxy.StartBeacon(key)      // 启动时按真 CLI 的上报序列注册会话与设备指纹
	proxy.StartModelRefresher() // 仅在生产入口启动, 单元测试不触网/不污染全局目录

	srv := server.NewServer(proxy)
	srv.SetPort(*port)
	srv.SetHost(*host)

	printStartupInfo(srv)

	srv.Start()
}

func versionText() string {
	latest, hasUpdate, err := update.LatestVersion(appVersion)
	if err != nil || !hasUpdate {
		return appVersion
	}
	return fmt.Sprintf("%s (最新版: %s)", appVersion, latest)
}

func printStartupInfo(srv *server.Server) {
	fmt.Println("")
	fmt.Println("========================================")
	fmt.Println("  CommandCode 代理服务")
	fmt.Println("========================================")
	fmt.Println("")
	fmt.Printf("  版本:        %s\n", versionText())
	fmt.Printf("  项目地址:    %s\n", repositoryURL)
	fmt.Printf("  绑定地址:    %s\n", srv.GetHost())
	fmt.Printf("  端口:        %s\n", srv.GetPort())
	fmt.Println("  上游接口:    https://api.commandcode.ai")
	fmt.Println("")
	fmt.Println("  接口列表:")
	fmt.Println("    POST /v1/chat/completions  (OpenAI 兼容)")
	fmt.Println("    POST /chat/completions     (OpenAI 兼容别名)")
	fmt.Println("    POST /v1/responses         (OpenAI Responses 兼容)")
	fmt.Println("    GET  /v1/models            (获取模型列表)")
	fmt.Println("    GET  /health               (健康检查)")
	fmt.Println("")
	fmt.Printf("  服务已启动: http://%s:%s\n", srv.GetHost(), srv.GetPort())
	fmt.Println("")
	fmt.Println("  按 Ctrl+C 停止服务")
	fmt.Println("========================================")
}
