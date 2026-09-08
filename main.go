package main

import (
	"flag"
	"fmt"

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
	apiKey := flag.String("api-key", "", "CommandCode API 密钥 (可选, 也可通过 Authorization 请求头传入)")
	projectSlug := flag.String("project-slug", "", "上报给上游的项目名 x-project-slug (默认: 当前目录名)")
	showVersion := flag.Bool("version", false, "打印版本号后退出")
	flag.Parse()

	if *showVersion {
		fmt.Println(versionText())
		return
	}

	proxy := proxy.NewProxy(*apiKey)
	proxy.Debug = debugLogging
	if *projectSlug != "" {
		proxy.SetProjectSlug(*projectSlug)
	}

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
