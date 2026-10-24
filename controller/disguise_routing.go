package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const DisguiseRoutingOptionKey = "AcceleratorDisguiseRoutingConfig"

var (
	disguiseSyncOnce   sync.Once
	disguiseSyncMu     sync.Mutex
	lastPushedConfigJS string
)

func acceleratorAdminBaseURL() string {
	raw := strings.TrimSpace(os.Getenv("NEWAPI_ACCELERATOR_URL"))
	if raw == "" {
		raw = "http://127.0.0.1:18317"
	}
	return strings.TrimRight(raw, "/")
}

func StartDisguiseRoutingSyncLoop() {
	disguiseSyncOnce.Do(func() {
		go func() {
			time.Sleep(2 * time.Second)
			for {
				syncDisguiseConfigFromOptionMapIfNeeded()
				time.Sleep(4 * time.Second)
			}
		}()
	})
}

func syncDisguiseConfigFromOptionMapIfNeeded() {
	common.OptionMapRWMutex.RLock()
	val := strings.TrimSpace(common.OptionMap[DisguiseRoutingOptionKey])
	common.OptionMapRWMutex.RUnlock()
	if val == "" {
		return
	}
	disguiseSyncMu.Lock()
	unchanged := (val == lastPushedConfigJS)
	disguiseSyncMu.Unlock()
	if unchanged {
		return
	}

	req, err := http.NewRequest(http.MethodPut, acceleratorAdminBaseURL()+"/__accelerator/routing-config", bytes.NewReader([]byte(val)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		disguiseSyncMu.Lock()
		lastPushedConfigJS = val
		disguiseSyncMu.Unlock()
	}
}

func GetDisguiseRoutingConfig(c *gin.Context) {
	StartDisguiseRoutingSyncLoop()
	syncDisguiseConfigFromOptionMapIfNeeded()

	// If an older cached browser bundle (pre-r13.14) calls GET /api/disguise-routing/config
	// without ?v=r1314, purge its HTTP disk cache and prevent AxiosResponse.config crash.
	if strings.EqualFold(c.GetHeader("Sec-Fetch-Mode"), "cors") && strings.TrimSpace(c.Query("v")) == "" {
		c.Header("Clear-Site-Data", `"cache"`)
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": "前端缓存已自动刷新，请按 Ctrl+F5 或点击页面刷新按钮加载最新控制台",
		})
		return
	}

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(acceleratorAdminBaseURL() + "/__accelerator/routing-config")
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"success": false,
			"message": "failed to query local accelerator routing-config: " + err.Error(),
		})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	c.Data(resp.StatusCode, "application/json; charset=utf-8", body)
}

func UpdateDisguiseRoutingConfig(c *gin.Context) {
	StartDisguiseRoutingSyncLoop()
	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil || len(bytes.TrimSpace(rawBody)) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "empty request body",
		})
		return
	}

	// Support both `{ "config": { ... } }` wrapper and direct `DisguiseRoutingConfig` object
	var wrapper map[string]json.RawMessage
	payloadToSend := rawBody
	if err := json.Unmarshal(rawBody, &wrapper); err == nil {
		if inner, ok := wrapper["config"]; ok && len(inner) > 2 {
			payloadToSend = inner
		}
	}

	req, err := http.NewRequest(http.MethodPut, acceleratorAdminBaseURL()+"/__accelerator/routing-config", bytes.NewReader(payloadToSend))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"success": false,
			"message": "failed to update local accelerator routing-config: " + err.Error(),
		})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsedResp struct {
			Success bool            `json:"success"`
			Config  json.RawMessage `json:"config"`
		}
		if err := json.Unmarshal(respBody, &parsedResp); err == nil && len(parsedResp.Config) > 2 {
			canonicalStr := string(parsedResp.Config)
			disguiseSyncMu.Lock()
			lastPushedConfigJS = canonicalStr
			disguiseSyncMu.Unlock()
			_ = model.UpdateOption(DisguiseRoutingOptionKey, canonicalStr)
		}
	}

	c.Data(resp.StatusCode, "application/json; charset=utf-8", respBody)
}

func ResetDisguiseRoutingConfig(c *gin.Context) {
	StartDisguiseRoutingSyncLoop()
	req, err := http.NewRequest(http.MethodPost, acceleratorAdminBaseURL()+"/__accelerator/routing-config/reset", nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsedResp struct {
			Success bool            `json:"success"`
			Config  json.RawMessage `json:"config"`
		}
		if err := json.Unmarshal(respBody, &parsedResp); err == nil && len(parsedResp.Config) > 2 {
			canonicalStr := string(parsedResp.Config)
			disguiseSyncMu.Lock()
			lastPushedConfigJS = canonicalStr
			disguiseSyncMu.Unlock()
			_ = model.UpdateOption(DisguiseRoutingOptionKey, canonicalStr)
		}
	}
	c.Data(resp.StatusCode, "application/json; charset=utf-8", respBody)
}

func SimulateDisguiseRouting(c *gin.Context) {
	StartDisguiseRoutingSyncLoop()
	syncDisguiseConfigFromOptionMapIfNeeded()
	rawBody, _ := io.ReadAll(c.Request.Body)
	req, err := http.NewRequest(http.MethodPost, acceleratorAdminBaseURL()+"/__accelerator/routing-simulate", bytes.NewReader(rawBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	c.Data(resp.StatusCode, "application/json; charset=utf-8", respBody)
}

func PreviewDisguiseRoutingTTFT(c *gin.Context) {
	StartDisguiseRoutingSyncLoop()
	syncDisguiseConfigFromOptionMapIfNeeded()
	rawBody, _ := io.ReadAll(c.Request.Body)
	method := http.MethodPost
	if c.Request.Method == http.MethodGet && len(bytes.TrimSpace(rawBody)) == 0 {
		method = http.MethodGet
	}
	req, err := http.NewRequest(method, acceleratorAdminBaseURL()+"/__accelerator/ttft-preview", bytes.NewReader(rawBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	c.Data(resp.StatusCode, "application/json; charset=utf-8", respBody)
}

func GetDisguiseCapturedSamples(c *gin.Context) {
	StartDisguiseRoutingSyncLoop()
	modelFilter := strings.TrimSpace(c.Query("model"))
	targetURL := acceleratorAdminBaseURL() + "/__accelerator/captured-requests"
	if modelFilter != "" {
		targetURL += "?model=" + url.QueryEscape(modelFilter)
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(targetURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	c.Data(resp.StatusCode, "application/json; charset=utf-8", respBody)
}

func ClearDisguiseCapturedSamples(c *gin.Context) {
	req, err := http.NewRequest(http.MethodDelete, acceleratorAdminBaseURL()+"/__accelerator/captured-requests", nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": err.Error()})
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	c.Data(resp.StatusCode, "application/json; charset=utf-8", respBody)
}

func ServeDisguiseRoutingAdminPage(c *gin.Context) {
	StartDisguiseRoutingSyncLoop()
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(disguiseAdminHTML))
}

const disguiseAdminHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8" />
<meta name="viewport" content="width=device-width, initial-scale=1.0" />
<title>NewAPI 首字延迟伪装 (TTFT) 与上游路由控制台</title>
<style>
  :root {
    --bg: #0f172a;
    --card: #1e293b;
    --card-alt: #0f172a;
    --border: #334155;
    --text: #f8fafc;
    --muted: #94a3b8;
    --primary: #3b82f6;
    --primary-hover: #2563eb;
    --emerald: #10b981;
    --amber: #f59e0b;
    --purple: #8b5cf6;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC", "Microsoft YaHei", sans-serif;
    background: var(--bg);
    color: var(--text);
    line-height: 1.5;
  }
  header {
    background: #1e293b;
    border-bottom: 1px solid var(--border);
    padding: 16px 24px;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }
  .container { max-width: 1200px; margin: 20px auto; padding: 0 20px; display: grid; gap: 20px; }
  .card { background: var(--card); border: 1px solid var(--border); border-radius: 12px; padding: 20px; }
  .card h2 { margin: 0 0 6px 0; font-size: 17px; display: flex; align-items: center; gap: 8px; }
  .card p.desc { margin: 0 0 16px 0; font-size: 13px; color: var(--muted); }
  .grid-2 { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
  .grid-4 { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; }
  @media (max-width: 900px) { .grid-2, .grid-4 { grid-template-columns: 1fr; } }
  .control-box { background: var(--card-alt); border: 1px solid var(--border); border-radius: 8px; padding: 12px; }
  .control-head { display: flex; justify-content: space-between; align-items: center; font-size: 13px; font-weight: 600; margin-bottom: 6px; }
  .control-sub { font-size: 11px; color: var(--muted); margin-top: 4px; }
  input[type="range"] { width: 100%; accent-color: var(--primary); }
  input[type="number"], input[type="text"], select {
    background: #0f172a; border: 1px solid var(--border); color: var(--text);
    padding: 6px 10px; border-radius: 6px; font-size: 13px;
  }
  button {
    background: var(--primary); color: #fff; border: none; padding: 8px 14px;
    border-radius: 8px; cursor: pointer; font-size: 13px; font-weight: 600;
  }
  button:hover { background: var(--primary-hover); }
  button.secondary { background: #334155; }
  button.emerald { background: var(--emerald); }
  .badge { display: inline-block; padding: 2px 8px; border-radius: 999px; font-size: 11px; font-weight: 600; }
  .badge.green { background: rgba(16,185,129,0.18); color: #34d399; border: 1px solid rgba(16,185,129,0.35); }
  .badge.gray { background: rgba(148,163,184,0.15); color: #cbd5e1; border: 1px solid rgba(148,163,184,0.3); }
  .chart-bars { display: flex; align-items: flex-end; gap: 4px; height: 150px; padding: 12px 8px 4px 8px; background: #0f172a; border-radius: 8px; border: 1px solid var(--border); }
  .bar-col { flex: 1; display: flex; flex-direction: column; align-items: center; height: 100%; justify-content: flex-end; }
  .bar-fill { width: 100%; border-radius: 4px 4px 0 0; transition: height 0.2s; min-height: 2px; }
  .bar-label { font-size: 10px; color: var(--muted); margin-top: 4px; writing-mode: horizontal-tb; white-space: nowrap; overflow: hidden; }
  .stat-pill { background: #0f172a; border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; }
  .stat-pill .k { font-size: 11px; color: var(--muted); }
  .stat-pill .v { font-size: 16px; font-weight: 700; margin-top: 2px; }
  #toast { position: fixed; right: 20px; bottom: 20px; padding: 12px 18px; border-radius: 8px; background: #10b981; color: #fff; font-weight: 600; display: none; z-index: 999; }
</style>
</head>
<body>
<header>
  <div>
    <h1 style="margin:0;font-size:19px;">⚡ NewAPI 首字延迟伪装 (TTFT) 与上游路由控制台</h1>
    <div style="font-size:12px;color:var(--muted);margin-top:2px;">三重数学分布随机叠加 (Log-Normal + Erlang/Gamma + Weibull) · 空回复自动自愈 · CPA1 全量直通优先</div>
  </div>
  <div style="display:flex;gap:8px;align-items:center;">
    <a href="/system-settings/request-policies/disguise" style="background:#334155;color:#fff;text-decoration:none;padding:8px 12px;border-radius:8px;font-size:13px;font-weight:600;">📋 返回管理员后台菜单页</a>
    <input id="adminTokenInput" type="password" placeholder="Admin Token (若已登录可留空)" style="width:200px;" />
    <input id="adminUserIdInput" type="text" placeholder="User-Id (如 1)" style="width:90px;" />
    <button class="secondary" onclick="loadAll()">🔄 刷新</button>
    <button class="emerald" onclick="saveConfig()">💾 保存并全节点热生效</button>
  </div>
</header>

<div class="container">
  <div class="card">
    <div style="display:flex;justify-content:space-between;align-items:center;flex-wrap:wrap;gap:8px;margin-bottom:10px;">
      <div>
        <h2>🎲 1. 首字响应延迟伪装 (TTFT 三重数学分布叠加调节器)</h2>
        <p class="desc">无需数学背景，直接拖动滑块调节「最快值 / 最常用核心值 / 主要上限 / 长尾范围」，系统自动混合三种概率分布消除固定指纹。</p>
      </div>
      <div style="display:flex;gap:6px;flex-wrap:wrap;">
        <button class="secondary" onclick="applyPreset('fast_core')">⚡ 推荐常用 (核心 2.5s · 85% &lt; 4s)</button>
        <button class="secondary" onclick="applyPreset('snappy')">🚀 极速响应 (核心 1.9s)</button>
        <button class="secondary" onclick="applyPreset('deep_think')">🧠 深度思考感 (核心 3.2s)</button>
      </div>
    </div>

    <div class="grid-2">
      <div style="display:grid;gap:10px;">
        <div class="control-box">
          <div class="control-head"><span>最快响应边界 (最快值)</span><span id="lbl_min_ms">1.2s</span></div>
          <input id="rng_min_ms" type="range" min="500" max="3500" step="100" oninput="onTtftInput('min_ms', this.value)" />
          <div class="control-sub">绝不低于此延迟（防止瞬间返回暴露缓存或转发特征，推荐 1.2s）</div>
        </div>
        <div class="control-box">
          <div class="control-head"><span>最期望核心常用值 (峰值中心)</span><span id="lbl_core_ms">2.5s</span></div>
          <input id="rng_core_ms" type="range" min="1400" max="5000" step="100" oninput="onTtftInput('core_ms', this.value)" />
          <div class="control-sub">最密集出现的黄金首字时间（推荐 2.2s ~ 2.8s 之间）</div>
        </div>
        <div class="control-box">
          <div class="control-head"><span>主要集中上限 (80%+ 请求低于此值)</span><span id="lbl_primary_upper_ms">4.0s</span></div>
          <input id="rng_primary_upper_ms" type="range" min="2500" max="7000" step="100" oninput="onTtftInput('primary_upper_ms', this.value)" />
          <div class="control-sub">绝大多数日常请求落在此上限之内（推荐 4.0s）</div>
        </div>
        <div class="control-box">
          <div class="control-head"><span>常规最慢边界 &amp; 偶发长尾上限</span><span id="lbl_tail_ms">常规 6.0s / 长尾 12.0s (5%)</span></div>
          <div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:8px;">
            <div><label style="font-size:11px;color:var(--muted);">常规上限(ms)</label><input id="num_normal_max_ms" type="number" step="200" style="width:100%" onchange="onTtftInput('normal_max_ms', this.value)" /></div>
            <div><label style="font-size:11px;color:var(--muted);">长尾上限(ms)</label><input id="num_tail_max_ms" type="number" step="500" style="width:100%" onchange="onTtftInput('tail_max_ms', this.value)" /></div>
            <div><label style="font-size:11px;color:var(--muted);">长尾占比(%)</label><input id="num_tail_pct" type="number" min="0" max="25" style="width:100%" onchange="onTtftInput('tail_probability_pct', this.value)" /></div>
          </div>
        </div>
        <div class="control-box">
          <div class="control-head"><span>离散随机度 (三重分布混合熵)</span><span id="lbl_spread">68%</span></div>
          <input id="rng_spread" type="range" min="10" max="100" step="2" oninput="onTtftInput('spread_randomness', this.value)" />
          <div class="control-sub">越高则三种数学分布混合跨度越宽、越难被统计学指纹识别</div>
        </div>
      </div>

      <div style="display:flex;flex-direction:column;justify-content:space-between;gap:12px;">
        <div class="grid-4" id="ttftSummaryStats"></div>
        <div>
          <div style="font-size:12px;color:var(--muted);margin-bottom:6px;display:flex;justify-content:space-between;">
            <span>📊 实时 1200 次采样概率分布直方图 (绿=核心峰值区 · 蓝=主分布区 · 紫=5%偶发长尾)</span>
            <button class="secondary" style="padding:2px 8px;font-size:11px;" onclick="refreshTtftPreview()">🎲 重新抽样预览</button>
          </div>
          <div class="chart-bars" id="ttftChartBars"></div>
        </div>
        <div class="control-box">
          <div style="font-size:12px;font-weight:600;margin-bottom:4px;">最近 16 次仿真请求首字延迟序列 (真实微秒级抖动预览):</div>
          <div id="ttftSampleSeries" style="font-family:monospace;font-size:12px;color:#93c5fd;word-break:break-all;"></div>
        </div>
      </div>
    </div>
  </div>

  <div class="card">
    <h2>🛡️ 2. 上游路由与 Argon 伪装开关 (默认已关闭 Argon 伪装，全量直连 CPA1)</h2>
    <p class="desc">当前 <code>astra</code> 与 <code>sol</code> 在 CPA1 主池成本低于 Argon，因此默认将 <code>gpt-6.1-sol</code>、<code>gpt-6-astra</code>、<code>gpt-5.6-sol</code> 等全部 100% 路由至 CPA1 原生主池，仅在您手动开启下方开关时启用 CPA2 Argon 伪装分流。</p>
    <div id="rulesSimpleContainer" style="display:grid;gap:12px;"></div>
  </div>
</div>

<div id="toast"></div>

<script>
let currentConfig = null;

function getAuthHeaders() {
  const token = document.getElementById('adminTokenInput').value.trim();
  const uid = document.getElementById('adminUserIdInput').value.trim() || localStorage.getItem('new_api_user') || '1';
  const h = { 'Content-Type': 'application/json', 'New-Api-User': uid };
  if (token) h['Authorization'] = 'Bearer ' + token;
  return h;
}

function showToast(msg) {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.style.display = 'block';
  setTimeout(() => { t.style.display = 'none'; }, 3200);
}

async function loadAll() {
  const resp = await fetch('/api/disguise-routing/config', { headers: getAuthHeaders(), credentials: 'include' });
  const data = await resp.json();
  currentConfig = data.config || data;
  const loadedTtft = currentConfig.ttft || currentConfig.ttft_distribution || {
    enabled: true, preset: 'fast_core', min_ms: 1200, core_ms: 2500,
    primary_upper_ms: 4000, normal_max_ms: 6000, tail_max_ms: 12000,
    tail_probability_pct: 5, spread_randomness: 68
  };
  currentConfig.ttft = loadedTtft;
  currentConfig.ttft_distribution = loadedTtft;
  renderControls();
  await refreshTtftPreview();
}

function renderControls() {
  const t = currentConfig.ttft || currentConfig.ttft_distribution;
  document.getElementById('rng_min_ms').value = t.min_ms;
  document.getElementById('lbl_min_ms').textContent = (t.min_ms / 1000).toFixed(1) + 's (' + t.min_ms + 'ms)';
  document.getElementById('rng_core_ms').value = t.core_ms;
  document.getElementById('lbl_core_ms').textContent = (t.core_ms / 1000).toFixed(1) + 's (' + t.core_ms + 'ms)';
  document.getElementById('rng_primary_upper_ms').value = t.primary_upper_ms;
  document.getElementById('lbl_primary_upper_ms').textContent = (t.primary_upper_ms / 1000).toFixed(1) + 's (' + t.primary_upper_ms + 'ms)';
  document.getElementById('num_normal_max_ms').value = t.normal_max_ms;
  document.getElementById('num_tail_max_ms').value = t.tail_max_ms;
  document.getElementById('num_tail_pct').value = t.tail_probability_pct;
  document.getElementById('lbl_tail_ms').textContent = '常规 ' + (t.normal_max_ms/1000).toFixed(1) + 's / 长尾 ' + (t.tail_max_ms/1000).toFixed(1) + 's (' + t.tail_probability_pct + '%)';
  document.getElementById('rng_spread').value = t.spread_randomness;
  document.getElementById('lbl_spread').textContent = t.spread_randomness + '%';

  const box = document.getElementById('rulesSimpleContainer');
  box.innerHTML = (currentConfig.rules || []).map((r, idx) => {
    const badge = r.enabled
      ? '<span class="badge green">✅ 已启用</span>'
      : '<span class="badge gray">⏸️ 默认已停用 (100% 走 CPA1 原生)</span>';
    return '<div class="control-box" style="display:flex;justify-content:space-between;align-items:center;gap:12px;">' +
      '<div>' +
        '<div style="font-weight:600;font-size:14px;">' + r.name + ' ' + badge + '</div>' +
        '<div style="font-size:12px;color:var(--muted);margin-top:4px;">匹配模型: <code>' + (r.model_patterns || []).join(', ') + '</code> | 说明: ' + (r.notes || '') + '</div>' +
      '</div>' +
      '<label style="display:flex;align-items:center;gap:6px;cursor:pointer;white-space:nowrap;font-size:13px;">' +
        '<input type="checkbox" ' + (r.enabled ? 'checked' : '') + ' onchange="toggleRule(' + idx + ', this.checked)" /> 启用此规则' +
      '</label>' +
    '</div>';
  }).join('');
}

function toggleRule(idx, enabled) {
  currentConfig.rules[idx].enabled = enabled;
  renderControls();
}

function onTtftInput(field, val) {
  currentConfig.ttft[field] = Number(val);
  currentConfig.ttft.preset = 'custom';
  currentConfig.ttft_distribution = currentConfig.ttft;
  renderControls();
  refreshTtftPreview();
}

function applyPreset(p) {
  const t = currentConfig.ttft;
  t.preset = p;
  if (p === 'fast_core') {
    Object.assign(t, { min_ms: 1200, core_ms: 2500, primary_upper_ms: 4000, normal_max_ms: 6000, tail_max_ms: 12000, tail_probability_pct: 5, spread_randomness: 68 });
  } else if (p === 'snappy') {
    Object.assign(t, { min_ms: 900, core_ms: 1900, primary_upper_ms: 3000, normal_max_ms: 4600, tail_max_ms: 9000, tail_probability_pct: 4, spread_randomness: 60 });
  } else if (p === 'deep_think') {
    Object.assign(t, { min_ms: 1500, core_ms: 3200, primary_upper_ms: 5000, normal_max_ms: 7500, tail_max_ms: 14000, tail_probability_pct: 8, spread_randomness: 75 });
  }
  currentConfig.ttft_distribution = t;
  renderControls();
  refreshTtftPreview();
}

async function refreshTtftPreview() {
  const resp = await fetch('/api/disguise-routing/ttft-preview', {
    method: 'POST', headers: getAuthHeaders(), credentials: 'include',
    body: JSON.stringify({ ttft: currentConfig.ttft, ttft_distribution: currentConfig.ttft, sample_count: 1200 })
  });
  const data = await resp.json();
  if (!data.summary) return;
  const s = data.summary;
  document.getElementById('ttftSummaryStats').innerHTML =
    '<div class="stat-pill"><div class="k">&lt; ' + (currentConfig.ttft_distribution.primary_upper_ms/1000).toFixed(1) + 's 主区间占比</div><div class="v" style="color:#34d399;">' + s.share_below_primary_upper_pct + '%</div></div>' +
    '<div class="stat-pill"><div class="k">中位数 (P50) / 均值</div><div class="v">' + (s.p50_ms/1000).toFixed(2) + 's / ' + (s.mean_ms/1000).toFixed(2) + 's</div></div>' +
    '<div class="stat-pill"><div class="k">最密集峰值区间</div><div class="v" style="color:#60a5fa;">' + s.peak_range_label + '</div></div>' +
    '<div class="stat-pill"><div class="k">偶发长尾 (&gt;' + (currentConfig.ttft_distribution.normal_max_ms/1000).toFixed(1) + 's)</div><div class="v" style="color:#c084fc;">' + s.share_in_tail_pct + '%</div></div>';

  const buckets = data.buckets || [];
  const maxPct = Math.max(1, ...buckets.map(b => b.pct));
  document.getElementById('ttftChartBars').innerHTML = buckets.map(b => {
    const h = Math.max(3, Math.round((b.pct / maxPct) * 115));
    const color = b.zone === 'core' ? '#10b981' : (b.zone === 'tail' ? '#a855f7' : (b.zone === 'secondary' ? '#f59e0b' : '#3b82f6'));
    return '<div class="bar-col" title="' + b.label + ': ' + b.pct + '% (' + b.count + '次)">' +
      '<div style="font-size:9px;color:var(--muted);margin-bottom:2px;">' + (b.pct >= 4 ? b.pct + '%' : '') + '</div>' +
      '<div class="bar-fill" style="height:' + h + 'px;background:' + color + ';"></div>' +
      '<div class="bar-label">' + (b.start_ms / 1000).toFixed(1) + 's</div>' +
    '</div>';
  }).join('');

  document.getElementById('ttftSampleSeries').textContent = (data.sample_series_ms || []).slice(0, 16).map(ms => (ms/1000).toFixed(3) + 's').join('  ➔  ');
}

async function saveConfig() {
  const resp = await fetch('/api/disguise-routing/config', {
    method: 'PUT', headers: getAuthHeaders(), credentials: 'include',
    body: JSON.stringify(currentConfig)
  });
  const data = await resp.json();
  if (data.config) {
    currentConfig = data.config;
    renderControls();
    showToast('✅ 配置已保存并实时同步生效！');
  }
}

window.addEventListener('DOMContentLoaded', loadAll);
</script>
</body>
</html>
`
