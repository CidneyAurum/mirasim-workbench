package gateway

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mirasim2api/internal/billing"
	"mirasim2api/internal/mirasim"
	"mirasim2api/internal/pool"
	"mirasim2api/internal/runtimecfg"
	"mirasim2api/internal/store"
)

const capacityRetriesHeader = "x-mirasim2api-capacity-retries"

// forward 执行核心转发：选号 → 占账号并发槽 → 签名转发 →
// 503 容量重试 / 401 换账号 → 透传（SSE tee 出 usage）→ 计费与日志。
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, endpoint string, norm *normResult, key *store.APIKey) {
	start := time.Now()
	upstreamPath := "/v1/" + endpoint
	accountMaxConc := 5
	if h.cfg != nil && h.cfg.AccountMaxConcurrency > 0 {
		accountMaxConc = h.cfg.AccountMaxConcurrency
	}

	log := &store.UsageLog{Model: norm.Model, Endpoint: upstreamPath}
	if key != nil {
		log.APIKeyID = key.ID
	}
	retries := 0
	defer func() {
		log.DurationMs = time.Since(start).Milliseconds()
		if h.billing != nil {
			h.billing.Record(log) // Record 内计算 log.Cost
		}
		keyName := ""
		if key != nil {
			keyName = key.Name
		}
		h.logger.Info("gateway request",
			"key", keyName,
			"account", log.AccountID,
			"model", log.Model,
			"endpoint", upstreamPath,
			"status", log.Status,
			"input_tokens", log.InputTokens,
			"output_tokens", log.OutputTokens,
			"cached_tokens", log.CachedTokens,
			"cost", log.Cost,
			"duration_ms", log.DurationMs,
			"retries", retries,
			"normalize", norm.Actions,
			"err", log.Err,
		)
	}()

	exclude := map[string]bool{}
	retried401 := false
	sawTransportErr := false
	for {
		entry := h.pool.Pick(exclude, norm.AffinityKey)
		if entry == nil {
			log.Err = "no available upstream account"
			if sawTransportErr {
				log.Status = http.StatusBadGateway
				writeError(w, endpoint, log.Status, "api_error", "upstream transport error: no available account left")
			} else {
				log.Status = http.StatusServiceUnavailable
				writeError(w, endpoint, log.Status, "api_error", "no available upstream account")
			}
			return
		}
		if !entry.TryAcquire(accountMaxConc) {
			exclude[entry.Account.ID] = true // 并发槽满，视为暂不可用换下一个
			continue
		}
		res := h.tryEntry(w, r, entry, endpoint, upstreamPath, norm.Body, norm.Stream, log)
		retries += res.capacityTries
		switch res.action {
		case actResponded:
			return
		case actSwitchAccount:
			exclude[entry.Account.ID] = true
			retries++
			if res.unauthorized {
				if retried401 { // 401 仅换账号重试一次，再失败则透传
					log.Status = res.status
					log.Err = "upstream 401 after account switch"
					h.writeUpstream(w, res.status, res.contentType, res.body)
					return
				}
				retried401 = true
			}
			if res.transportErr {
				sawTransportErr = true
			}
			continue
		}
	}
}

type attemptAction int

const (
	actResponded     attemptAction = iota // 已给客户端写响应（成功或如实透传）
	actSwitchAccount                      // 换账号重试（401 / 传输错误）
)

type attemptResult struct {
	action        attemptAction
	unauthorized  bool
	transportErr  bool
	capacityTries int
	status        int
	contentType   string
	body          []byte
}

// tryEntry 在单个账号上尝试请求（503 model_capacity_exhausted 时同账号
// 同模型退避重试，不报账号故障）。调用方已持有 entry 并发槽，本函数负责释放。
func (h *Handler) tryEntry(w http.ResponseWriter, r *http.Request, entry *pool.Entry,
	endpoint, upstreamPath string, body []byte, clientStream bool,
	log *store.UsageLog) (res attemptResult) {
	defer entry.Release()
	log.AccountID = entry.Account.ID
	ctx := r.Context()

	capacityRetries, backoffMS := 2, 1200
	if h.cfg != nil {
		capacityRetries = h.cfg.CapacityRetries
		backoffMS = h.cfg.CapacityBackoffMS
	}
	// 运行时设置优先，环境变量兜底（非法值忽略）
	if v := h.runtimeSetting(runtimecfg.KeyCapacityRetries); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			capacityRetries = n
		}
	}
	if v := h.runtimeSetting(runtimecfg.KeyCapacityBackoffMS); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			backoffMS = n
		}
	}

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		req, err := entry.Client.SignedRequest(ctx, http.MethodPost, upstreamPath, body)
		if err != nil {
			h.pool.ReportFailure(entry, err)
			res.action = actSwitchAccount
			res.transportErr = true
			return res
		}
		resp, err = h.http.Do(req)
		if err != nil {
			if ctx.Err() != nil { // 客户端断开，不再重试
				res.action = actResponded
				log.Status = 499
				log.Err = "client disconnected: " + err.Error()
				return res
			}
			h.pool.ReportFailure(entry, err)
			res.action = actSwitchAccount
			res.transportErr = true
			log.Err = "upstream transport error: " + err.Error()
			return res
		}
		// 503 model_capacity_exhausted：同账号同模型退避重试
		if resp.StatusCode == http.StatusServiceUnavailable {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(b))
			if bytes.Contains(b, []byte("model_capacity_exhausted")) && attempt < capacityRetries {
				res.capacityTries++
				if !sleepCtx(ctx, time.Duration(backoffMS*(1<<attempt))*time.Millisecond) {
					res.action = actResponded
					log.Status = 499
					log.Err = "client disconnected during capacity backoff"
					return res
				}
				continue
			}
		}
		break
	}
	status := resp.StatusCode
	contentType := resp.Header.Get("Content-Type")

	switch {
	case status == http.StatusUnauthorized:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		entry.Client.InvalidateTicket()
		res.action = actSwitchAccount
		res.unauthorized = true
		res.status, res.contentType, res.body = status, contentType, b
		return res

	case status >= 200 && status < 300:
		h.pool.ReportSuccess(entry)
		log.Status = status
		var usage billing.Usage
		if clientStream && strings.Contains(contentType, "text/event-stream") {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			if res.capacityTries > 0 {
				w.Header().Set(capacityRetriesHeader, strconv.Itoa(res.capacityTries))
			}
			w.WriteHeader(status)
			usage = h.teeStream(w, resp.Body, endpoint)
		} else {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
			resp.Body.Close()
			usage = parseUsage(endpoint, b)
			if res.capacityTries > 0 {
				w.Header().Set(capacityRetriesHeader, strconv.Itoa(res.capacityTries))
			}
			h.writeUpstream(w, status, contentType, b)
		}
		log.InputTokens = usage.Input
		log.OutputTokens = usage.Output
		log.CachedTokens = usage.Cached
		res.action = actResponded
		return res

	default:
		// 容量重试用尽的 503：透传，不调用 ReportFailure（不是账号故障）。
		// 其他 ≥500：ReportFailure（pool 内部对 5xx 不熔断）后如实透传。
		// 4xx（除 401）：如实透传并 ReportFailure。
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		capacityExhausted := status == http.StatusServiceUnavailable &&
			bytes.Contains(b, []byte("model_capacity_exhausted"))
		if !capacityExhausted {
			h.pool.ReportFailure(entry, &mirasim.HTTPError{StatusCode: status, Body: string(b)})
		}
		log.Status = status
		if capacityExhausted {
			log.Err = "model_capacity_exhausted"
		} else {
			log.Err = http.StatusText(status)
		}
		h.writeUpstream(w, status, contentType, b)
		res.action = actResponded
		return res
	}
}

// writeUpstream 透传上游状态码、Content-Type 与响应体。
func (h *Handler) writeUpstream(w http.ResponseWriter, status int, contentType string, body []byte) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// teeStream 把上游 SSE 直通客户端（逐行 flush），同时只保留含 usage
// 的事件块交给用量解析器。上游 body 由本函数关闭。
func (h *Handler) teeStream(w http.ResponseWriter, body io.ReadCloser, endpoint string) billing.Usage {
	defer body.Close()
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 64*1024)
	var usageBuf, eventBuf []byte
	flushEvent := func() {
		if len(eventBuf) > 0 && bytes.Contains(eventBuf, []byte(`"usage"`)) {
			usageBuf = append(usageBuf, eventBuf...)
		}
		eventBuf = eventBuf[:0]
	}
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = w.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
			eventBuf = append(eventBuf, line...)
			if isBlankLine(line) {
				flushEvent()
			}
		}
		if err != nil {
			break
		}
	}
	flushEvent()
	return parseUsage(endpoint, usageBuf)
}

func isBlankLine(line []byte) bool {
	return len(bytes.TrimRight(line, "\r\n")) == 0
}

// parseUsage 按端点家族提取 token 用量。
func parseUsage(endpoint string, data []byte) billing.Usage {
	switch endpoint {
	case epMessages:
		return billing.ParseAnthropicUsage(data)
	case epResponses:
		return billing.ParseResponsesUsage(data)
	default:
		return billing.ParseChatUsage(data)
	}
}

// sleepCtx 睡眠 d；ctx 取消时返回 false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
