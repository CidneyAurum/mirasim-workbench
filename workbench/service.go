package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (a *app) recordPath() string { return filepath.Join(a.repo, "data", "gateway-process.json") }
func (a *app) binaryPath() string { return filepath.Join(a.repo, "mirasim2api.exe") }
func (a *app) record() processRecord {
	b, _ := os.ReadFile(a.recordPath())
	var rec processRecord
	_ = json.Unmarshal(b, &rec)
	return rec
}
func (a *app) isOwned() bool {
	r := a.record()
	return r.Port == a.gateway && ownedProcess(r, a.binaryPath(), false)
}
func (a *app) health() (map[string]any, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.gateway+"/health", nil)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	var v map[string]any
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&v) != nil {
		return nil, false
	}
	_, hasAccounts := v["accounts"]
	return v, v["status"] == "ok" && hasAccounts
}

func (a *app) startGateway() error {
	a.serviceMu.Lock()
	defer a.serviceMu.Unlock()
	return a.startLocked()
}
func (a *app) startLocked() error {
	previous := a.record()
	if previous.Port != a.gateway && ownedProcess(previous, a.binaryPath(), false) {
		return errors.New("本部署已有网关在其他端口运行，请先用原端口的工作台停止它")
	}
	if a.isOwned() {
		if _, ok := a.health(); ok {
			return nil
		}
		return errors.New("网关进程仍在运行但健康检查失败，请查看日志或重启")
	}
	// An existing process from a different deployment may share the filename.
	// Never reuse or terminate it based only on the port or executable name.
	address := strings.TrimPrefix(a.gateway, "http://")
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("网关端口 %s 被占用；未操作占用进程", address)
	}
	_ = ln.Close()
	f, err := os.OpenFile(filepath.Join(a.repo, "logs", "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	cmd := exec.Command(a.binaryPath())
	cmd.Dir = a.repo
	cmd.Stdout, cmd.Stderr = f, f
	detachProcess(cmd)
	_, port, _ := net.SplitHostPort(address)
	// Keep this deployment isolated from environment variables of other gateways.
	managed := "|HOST|PORT|DATA_DIR|MASTER_KEY|ADMIN_PASSWORD|GATEWAY_KEY_REQUIRED|RELAY_URL|AUTH_URL|UPSTREAM_PROXY|CLAUDE_CLOAK_MODE|CAPACITY_RETRIES|CAPACITY_BACKOFF_MS|ACCOUNT_MAX_CONCURRENCY|MIRASIM_CLIENT_VERSION|MIRASIM_SEAL_PUBKEY|"
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if !strings.Contains(managed, "|"+strings.ToUpper(key)+"|") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "HOST=127.0.0.1", "PORT="+port, "DATA_DIR="+filepath.Join(a.repo, "data"), "ADMIN_PASSWORD="+a.password, "GATEWAY_KEY_REQUIRED=true")
	if err = cmd.Start(); err != nil {
		f.Close()
		return err
	}
	stamp, err := inspectProcess(uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		f.Close()
		return err
	}
	rec := processRecord{PID: uint32(cmd.Process.Pid), Created: stamp, Port: a.gateway}
	b, _ := json.Marshal(rec)
	if err = os.WriteFile(a.recordPath(), b, 0600); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		f.Close()
		return err
	}
	go func() { _ = cmd.Wait(); _ = f.Close() }()
	log.Printf("gateway started pid=%d", rec.PID)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := a.health(); ok {
			return nil
		}
		if !ownedProcess(rec, a.binaryPath(), false) {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	return errors.New("网关未通过健康检查，请在「运行日志」查看原因")
}

func (a *app) stopLocked() error {
	rec := a.record()
	if !a.isOwned() {
		if _, ok := a.health(); ok {
			return errors.New("端口由非本工作台管理的进程提供服务，拒绝停止")
		}
		return nil
	}
	if !ownedProcess(rec, a.binaryPath(), true) {
		return errors.New("停止失败：进程身份改变或无法终止")
	}
	_ = os.Remove(a.recordPath())
	a.sessionMu.Lock()
	a.cookie, a.cookieUntil = nil, time.Time{}
	a.sessionMu.Unlock()
	log.Printf("gateway stopped pid=%d", rec.PID)
	return nil
}

func (a *app) handleState(w http.ResponseWriter, r *http.Request) {
	health, ready := a.health()
	owned := a.isOwned()
	a.stateMu.Lock()
	lastError := a.lastError
	a.stateMu.Unlock()
	jsonReply(w, 200, map[string]any{"version": version, "repo": a.repo, "gateway": a.gateway, "workbench": "http://" + a.addr,
		"running": ready && owned, "owned": owned, "port_responding": ready, "health": health, "error": lastError, "desktop_installed": desktopInstalled()})
}
func (a *app) handleService(w http.ResponseWriter, r *http.Request) {
	a.serviceMu.Lock()
	defer a.serviceMu.Unlock()
	var err error
	switch r.PathValue("action") {
	case "start":
		err = a.startLocked()
	case "stop":
		err = a.stopLocked()
	case "restart":
		if err = a.stopLocked(); err == nil {
			err = a.startLocked()
		}
	case "quit":
		if err = a.stopLocked(); err == nil && a.shutdown != nil {
			defer a.shutdown()
		}
	default:
		fail(w, 404, errors.New("未知服务操作"))
		return
	}
	a.setError(err)
	if err != nil {
		fail(w, 409, err)
		return
	}
	jsonReply(w, 200, map[string]bool{"ok": true})
}
