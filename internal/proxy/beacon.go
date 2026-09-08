package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/dev2k6/command-code-proxy-server/internal/version"
)

// beacon reproduces the three API calls a real CLI process makes at startup,
// observed via tools/tap (order and per-endpoint header templates preserved;
// the transport emits the wire shapes):
//
//	GET  /alpha/whoami              identity/key validation
//	POST /alpha/lifecycle-events    {"eventType":"cli_session_exists",...} with
//	                                the process-level sess_ id
//	POST /alpha/fingerprint/record  device fingerprint (needs local fingerprint.json;
//	                                produced by tools/recapture.mjs from a tap capture)
//
// Without these the upstream sees a session that emits generate traffic with
// no lifecycle birth event and no device registration — an absence tell.

type lifecycleEvent struct {
	EventType string `json:"eventType"`
	Metadata  struct {
		SessionID  string `json:"sessionId"`
		CLIVersion string `json:"cliVersion"`
		Mode       string `json:"mode"`
		OS         string `json:"os"`
	} `json:"metadata"`
}

func nodeOSArch() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return nodePlatform() + "-" + arch
}

// StartBeacon fires the startup sequence at most once per process. Safe to
// call on every request; the first call with a usable key wins. main.go calls
// it once at startup when a server key is configured; the chat handler calls
// it per request so a proxy that gets keys from clients still beacons.
func (p *Proxy) StartBeacon(apiKey string) {
	if apiKey == "" {
		return
	}
	p.beaconOnce.Do(func() {
		go p.runBeacon(apiKey)
	})
}

func (p *Proxy) runBeacon(apiKey string) {
	// Give the first generate request the connection's head start; the CLI's
	// beacon happens before any chat anyway, but ordering by microseconds is
	// invisible to the server while blocking user traffic would not be.
	time.Sleep(2 * time.Second)

	// 1. whoami (GET, no body).
	if err := p.beaconCall(http.MethodGet, "/alpha/whoami", nil, apiKey); err != nil {
		log.Printf("[信标] whoami 失败: %v", err)
		return
	}

	// 2. lifecycle-events: cli_session_exists for this process session.
	ev := lifecycleEvent{EventType: "cli_session_exists"}
	ev.Metadata.SessionID = p.identity.processSess
	ev.Metadata.CLIVersion = version.GetCommandCodeVersion()
	ev.Metadata.Mode = "non-interactive"
	ev.Metadata.OS = nodeOSArch()
	body, _ := json.Marshal(ev)
	if err := p.beaconCall(http.MethodPost, "/alpha/lifecycle-events", body, apiKey); err != nil {
		log.Printf("[信标] lifecycle-events 失败: %v", err)
	}

	// 3. fingerprint/record — only when the local capture file exists.
	fp, err := os.ReadFile(fingerprintFile)
	if err != nil {
		log.Printf("[信标] 未找到 %s, 跳过设备指纹上报 (tools/recapture.mjs 可生成)", fingerprintFile)
		return
	}
	if !json.Valid(fp) {
		log.Printf("[信标] %s 不是合法 JSON, 跳过设备指纹上报", fingerprintFile)
		return
	}
	if err := p.beaconCall(http.MethodPost, "/alpha/fingerprint/record", fp, apiKey); err != nil {
		log.Printf("[信标] fingerprint/record 失败: %v", err)
	} else {
		log.Printf("[信标] 启动信标已上报 (whoami/lifecycle/fingerprint)")
	}
}

func (p *Proxy) beaconCall(method, path string, body []byte, apiKey string) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(p.beaconCtx(), method, p.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-command-code-version", version.GetCommandCodeVersion())
	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s -> %d", path, resp.StatusCode)
	}
	return nil
}
