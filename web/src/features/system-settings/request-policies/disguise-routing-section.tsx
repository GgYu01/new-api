/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
import { useCallback, useEffect, useMemo, useState } from 'react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

interface UpstreamPoolConfig {
  id: string
  name: string
  base_url: string
  api_key: string
  enabled: boolean
  description: string
}

interface EffortMappingEntry {
  downstream_efforts: string[]
  upstream_model_override: string
  upstream_effort: string
  thinking_budget: number | null
}

interface PoolTargetWeight {
  target_id: string
  pool_id: string
  weight: number
  upstream_model: string
  disguise_enabled: boolean
  protocol_family: string
  masquerade_model: string
  masquerade_provider: string
  inject_identity_prompt: boolean
  scrub_response_identity: boolean
  effort_map: EffortMappingEntry[]
}

interface StickySessionPolicy {
  enabled: boolean
  min_soft_turns: number
  max_soft_turns: number
  ttl_seconds: number
  failover_on_error: boolean
}

interface EffortRouteOverride {
  downstream_efforts: string[]
  mode: 'weighted_mix' | 'forced_disguise' | 'direct_only'
  targets: PoolTargetWeight[]
  enabled: boolean
}

interface ModelRoutingRule {
  id: string
  name: string
  enabled: boolean
  priority: number
  model_patterns: string[]
  mode: 'weighted_mix' | 'forced_disguise' | 'direct_only'
  sticky_session: StickySessionPolicy
  targets: PoolTargetWeight[]
  effort_route_overrides?: EffortRouteOverride[]
  notes: string
}

interface RequestCapturePolicy {
  enabled: boolean
  max_samples_per_model: number
  max_total_samples: number
  ttl_seconds: number
  max_body_preview_bytes: number
  redact_auth_headers: boolean
}

interface TtftDistributionConfig {
  enabled: boolean
  preset: string
  min_ms: number
  core_ms: number
  primary_upper_ms: number
  normal_max_ms: number
  tail_max_ms: number
  tail_probability_pct: number
  spread_randomness: number
}

interface DisguiseRoutingConfig {
  version: number
  updated_at_unix: number
  global_enabled: boolean
  force_disguise_headers: string[]
  force_disguise_model_suffixes: string[]
  pools: UpstreamPoolConfig[]
  rules: ModelRoutingRule[]
  capture_policy?: RequestCapturePolicy
  ttft?: TtftDistributionConfig
  ttft_distribution?: TtftDistributionConfig
}

interface TtftBucket {
  label: string
  start_ms: number
  end_ms: number
  count: number
  pct: number
  zone: 'core' | 'primary' | 'secondary' | 'tail'
}

interface TtftPreviewData {
  summary: {
    sample_count: number
    min_ms: number
    p10_ms: number
    p25_ms: number
    p50_ms: number
    mean_ms: number
    p75_ms: number
    p90_ms: number
    p95_ms: number
    max_ms: number
    peak_range_label: string
    share_below_primary_upper_pct: number
    share_in_core_band_pct: number
    share_in_normal_range_pct: number
    share_in_tail_pct: number
  }
  buckets: TtftBucket[]
  sample_series_ms: number[]
}

interface CapturedSample {
  id: string
  captured_at_unix: number
  expires_at_unix: number
  endpoint_path: string
  downstream_model: string
  downstream_effort: string
  matched_rule_id: string
  routing_mode: string
  selected_pool_id: string
  upstream_model: string
  upstream_effort: string
  disguise_enabled: boolean
  session_key: string
  client_headers: Record<string, string>
  body_structure_summary: unknown
  raw_body_preview: string
  rewritten_body_preview: string
  body_total_bytes: number
}

const DEFAULT_TTFT_CONFIG: TtftDistributionConfig = {
  enabled: true,
  preset: 'fast_core',
  min_ms: 1200,
  core_ms: 2500,
  primary_upper_ms: 4000,
  normal_max_ms: 6000,
  tail_max_ms: 12000,
  tail_probability_pct: 5,
  spread_randomness: 68,
}

// Local deterministic 3-distribution superposition preview generator for instant slider feedback
function computeLocalTtftPreview(raw: TtftDistributionConfig, seedOffset: number): TtftPreviewData {
  const min_ms = Math.max(300, Math.min(8000, Number(raw.min_ms) || 1200))
  const core_ms = Math.max(min_ms + 300, Math.min(12000, Number(raw.core_ms) || 2500))
  const primary_upper_ms = Math.max(core_ms + 400, Math.min(16000, Number(raw.primary_upper_ms) || 4000))
  const normal_max_ms = Math.max(primary_upper_ms + 500, Math.min(25000, Number(raw.normal_max_ms) || 6000))
  const tail_max_ms = Math.max(normal_max_ms + 1000, Math.min(45000, Number(raw.tail_max_ms) || 12000))
  const tail_probability_pct = Math.max(0, Math.min(35, Number(raw.tail_probability_pct) ?? 5))
  const spread_randomness = Math.max(5, Math.min(100, Number(raw.spread_randomness) ?? 68))

  let s = (0x9e3779b9 + seedOffset * 7919) >>> 0
  const rand01 = () => {
    s = (s + 0x6d2b79f5) >>> 0
    let t = Math.imul(s ^ (s >>> 15), 1 | s)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return (((t ^ (t >>> 14)) >>> 0) + 0.5) / 4294967296.0
  }
  const randNormal = () => {
    const u1 = Math.max(1e-7, rand01())
    const u2 = rand01()
    return Math.sqrt(-2 * Math.log(u1)) * Math.cos(2 * Math.PI * u2)
  }
  const randErlang = (k: number) => {
    let sum = 0
    for (let i = 0; i < k; i++) {
      sum -= Math.log(Math.max(1e-7, rand01()))
    }
    return sum
  }

  const n = 1200
  const spread = spread_randomness / 100.0
  const deltaCore = Math.max(350, core_ms - min_ms)
  const samples: number[] = []
  let prevMs = core_ms

  for (let i = 0; i < n; i++) {
    if (!raw.enabled) {
      samples.push(min_ms)
      continue
    }
    const e1 = -Math.log(Math.max(1e-7, rand01()))
    const e2 = -Math.log(Math.max(1e-7, rand01()))
    const e3 = -Math.log(Math.max(1e-7, rand01()))
    const esum = e1 + e2 + e3
    const w1 = e1 / esum
    const w2 = e2 / esum
    const w3 = e3 / esum

    // 1. Shifted Log-Normal
    const sigma1 = 0.18 + 0.24 * spread
    const mu1 = Math.log(deltaCore) + 0.45 * sigma1 * sigma1
    const d1 = min_ms + Math.exp(mu1 + sigma1 * randNormal())

    // 2. 5-stage Gamma / Erlang
    const theta2 = (deltaCore * (0.94 + 0.14 * spread)) / 4.25
    const d2 = min_ms + theta2 * randErlang(5)

    // 3. Weibull
    const k3 = Math.max(1.45, 2.75 - 1.05 * spread)
    const scale3 = deltaCore / Math.pow(0.68, 1 / k3)
    const d3 = min_ms + scale3 * Math.pow(-Math.log(Math.max(1e-7, rand01())), 1 / k3)

    let x = w1 * d1 + w2 * d2 + w3 * d3
    const wave =
      Math.sin((i + 1) * 0.7182818) * 0.45 +
      Math.sin((i + 1) * 0.3141592 + 1.1) * 0.35 +
      Math.cos((i + 1) * 0.1618033 + 2.4) * 0.2
    x += wave * (95 + 160 * spread)

    const isTail = rand01() * 100 < tail_probability_pct
    if (isTail) {
      const tailSpan = Math.max(800, tail_max_ms - normal_max_ms)
      const u = Math.pow(rand01(), 0.78) * 0.72 + rand01() * 0.28
      x = normal_max_ms + tailSpan * Math.min(0.995, Math.max(0.02, u))
    } else if (x > primary_upper_ms) {
      const over = x - primary_upper_ms
      const secSpan = Math.max(400, normal_max_ms - primary_upper_ms)
      x = primary_upper_ms + secSpan * (1 - Math.exp(-over / (secSpan * (0.85 + 0.4 * spread))))
    }

    if (Math.abs(x - prevMs) < 130 && Math.abs(x - core_ms) > 420) {
      const nudge = (140 + rand01() * 190) * (rand01() > 0.5 ? 1 : -1)
      x += nudge
    }
    x = Math.max(min_ms, Math.min(isTail ? tail_max_ms : normal_max_ms, x))
    prevMs = x
    samples.push(Math.round(x))
  }

  const sorted = [...samples].sort((a, b) => a - b)
  const pctAt = (p: number) => sorted[Math.min(n - 1, Math.max(0, Math.round((n - 1) * p)))]
  const mean = Math.round(sorted.reduce((a, b) => a + b, 0) / n)
  const belowPrimary = sorted.filter((v) => v <= primary_upper_ms).length
  const inCore = sorted.filter((v) => Math.abs(v - core_ms) <= 450).length
  const inNormal = sorted.filter((v) => v <= normal_max_ms).length
  const inTail = sorted.filter((v) => v > normal_max_ms).length

  const step = Math.max(250, Math.floor(Math.max(2400, tail_max_ms - min_ms) / 18))
  const buckets: TtftBucket[] = []
  let cur = Math.floor(min_ms / 200) * 200
  let peakLabel = ''
  let peakCount = -1

  while (cur < tail_max_ms) {
    const end = Math.min(tail_max_ms + 1, cur + step)
    const count = sorted.filter((v) => v >= cur && v < end).length
    const pct = Math.round((count / n) * 1000) / 10
    const label = `${(cur / 1000).toFixed(1)}s-${(end / 1000).toFixed(1)}s`
    if (count > peakCount) {
      peakCount = count
      peakLabel = label
    }
    const mid = (cur + end) / 2
    const zone: TtftBucket['zone'] =
      mid > normal_max_ms
        ? 'tail'
        : mid > primary_upper_ms
          ? 'secondary'
          : Math.abs(mid - core_ms) <= 450
            ? 'core'
            : 'primary'
    buckets.push({ label, start_ms: cur, end_ms: end, count, pct, zone })
    cur = end
  }

  return {
    summary: {
      sample_count: n,
      min_ms: pctAt(0),
      p10_ms: pctAt(0.1),
      p25_ms: pctAt(0.25),
      p50_ms: pctAt(0.5),
      mean_ms: mean,
      p75_ms: pctAt(0.75),
      p90_ms: pctAt(0.9),
      p95_ms: pctAt(0.95),
      max_ms: pctAt(1),
      peak_range_label: peakLabel,
      share_below_primary_upper_pct: Math.round((belowPrimary / n) * 1000) / 10,
      share_in_core_band_pct: Math.round((inCore / n) * 1000) / 10,
      share_in_normal_range_pct: Math.round((inNormal / n) * 1000) / 10,
      share_in_tail_pct: Math.round((inTail / n) * 1000) / 10,
    },
    buckets,
    sample_series_ms: samples.slice(0, 16),
  }
}

export function DisguiseRoutingSection() {
  const userRole = useAuthStore((s) => s.user?.role ?? ROLE.COMMON)
  const isRoot = userRole >= ROLE.ROOT

  const [config, setConfig] = useState<DisguiseRoutingConfig | null>(null)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [previewSeed, setPreviewSeed] = useState(1)
  const [serverPreview, setServerPreview] = useState<TtftPreviewData | null>(null)
  const [showAdvanced, setShowAdvanced] = useState(false)

  // Optional simulation & samples inside Advanced drawer
  const [simModel, setSimModel] = useState('gpt-6.1-sol')
  const [simEffort, setSimEffort] = useState('xhigh')
  const [simForceArgon, setSimForceArgon] = useState(false)
  const [simOutput, setSimOutput] = useState<string>('')
  const [samples, setSamples] = useState<CapturedSample[]>([])
  const [selectedSampleIdx, setSelectedSampleIdx] = useState<number | null>(null)

  const loadConfig = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.get<{
        success: boolean
        config?: DisguiseRoutingConfig
        message?: string
      }>('/api/disguise-routing/config')
      if (res.config) {
        const loadedTtft =
          res.config.ttft ?? res.config.ttft_distribution ?? DEFAULT_TTFT_CONFIG
        res.config.ttft = { ...loadedTtft }
        res.config.ttft_distribution = { ...loadedTtft }
        setConfig(res.config)
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '加载配置失败')
    } finally {
      setLoading(false)
    }
  }, [])

  const loadSamples = useCallback(async () => {
    try {
      const res = await api.get<{
        success: boolean
        samples?: CapturedSample[]
      }>('/api/disguise-routing/samples')
      setSamples(res.samples ?? [])
    } catch {
      // ignore
    }
  }, [])

  useEffect(() => {
    if (isRoot) {
      void loadConfig()
    }
  }, [isRoot, loadConfig])

  const ttft =
    config?.ttft ?? config?.ttft_distribution ?? DEFAULT_TTFT_CONFIG

  const activePreview = useMemo(() => {
    return serverPreview ?? computeLocalTtftPreview(ttft, previewSeed)
  }, [ttft, previewSeed, serverPreview])

  const updateTtftField = <K extends keyof TtftDistributionConfig>(
    key: K,
    val: TtftDistributionConfig[K]
  ) => {
    if (!config) return
    setServerPreview(null)
    const next: TtftDistributionConfig = {
      ...(config.ttft ?? config.ttft_distribution ?? DEFAULT_TTFT_CONFIG),
      [key]: val,
      preset: key === 'preset' ? String(val) : 'custom',
    }
    setConfig({
      ...config,
      ttft: next,
      ttft_distribution: next,
    })
  }

  const applyTtftPreset = (preset: 'fast_core' | 'snappy' | 'deep_think') => {
    if (!config) return
    setServerPreview(null)
    let next: TtftDistributionConfig = { ...DEFAULT_TTFT_CONFIG, preset }
    if (preset === 'snappy') {
      next = {
        enabled: true,
        preset: 'snappy',
        min_ms: 900,
        core_ms: 1900,
        primary_upper_ms: 3000,
        normal_max_ms: 4600,
        tail_max_ms: 9000,
        tail_probability_pct: 4,
        spread_randomness: 60,
      }
    } else if (preset === 'deep_think') {
      next = {
        enabled: true,
        preset: 'deep_think',
        min_ms: 1500,
        core_ms: 3200,
        primary_upper_ms: 5000,
        normal_max_ms: 7500,
        tail_max_ms: 14000,
        tail_probability_pct: 8,
        spread_randomness: 75,
      }
    }
    setConfig({
      ...config,
      ttft: next,
      ttft_distribution: next,
    })
    toast.success('已套用 TTFT 分布预设，点击右上角「保存并热生效」即可应用')
  }

  const fetchBackendTtftPreview = async () => {
    if (!config) return
    try {
      const currentTtft =
        config.ttft ?? config.ttft_distribution ?? DEFAULT_TTFT_CONFIG
      const res = await api.post<TtftPreviewData & { success?: boolean }>(
        '/api/disguise-routing/ttft-preview',
        {
          ttft: currentTtft,
          ttft_distribution: currentTtft,
          sample_count: 1200,
        }
      )
      if (res && res.summary && res.buckets) {
        setServerPreview(res)
        toast.success('已从 Rust 加速器实时抽样 1200 次生成分布图')
      } else {
        setPreviewSeed((x) => x + 1)
      }
    } catch {
      setPreviewSeed((x) => x + 1)
    }
  }

  const handleSave = async () => {
    if (!config) return
    setSaving(true)
    try {
      const res = await api.put<{
        success: boolean
        config?: DisguiseRoutingConfig
        message?: string
      }>('/api/disguise-routing/config', config)
      if (!res.success || !res.config) {
        toast.error(res.message || '保存失败')
        return
      }
      setConfig(res.config)
      toast.success('配置已保存并同步至本地 Rust 加速器与 PostgreSQL 数据库')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  const handleReset = async () => {
    if (!window.confirm('确定恢复默认设置（TTFT 核心 2.5s 三分布叠加 + 关闭 Argon 伪装全走 CPA1）吗？')) {
      return
    }
    setSaving(true)
    try {
      const res = await api.post<{
        success: boolean
        config?: DisguiseRoutingConfig
      }>('/api/disguise-routing/reset')
      if (res.config) {
        setConfig(res.config)
        setServerPreview(null)
        toast.success('已恢复默认配置')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '重置失败')
    } finally {
      setSaving(false)
    }
  }

  const toggleRuleEnabled = (ruleIdx: number, enabled: boolean) => {
    if (!config) return
    const nextRules = config.rules.map((r, i) => (i === ruleIdx ? { ...r, enabled } : r))
    setConfig({ ...config, rules: nextRules })
  }

  const updateMixArgonWeight = (ruleIdx: number, argonWeight: number) => {
    if (!config) return
    const clampedArgon = Math.max(0, Math.min(100, argonWeight))
    const cpa1Weight = 100 - clampedArgon
    const nextRules = config.rules.map((r, i) => {
      if (i !== ruleIdx) return r
      const nextTargets = r.targets.map((t) => {
        if (t.pool_id === 'cpa2') return { ...t, weight: clampedArgon }
        if (t.pool_id === 'cpa1') return { ...t, weight: cpa1Weight }
        return t
      })
      return { ...r, targets: nextTargets }
    })
    setConfig({ ...config, rules: nextRules })
  }

  const runSimulation = async () => {
    try {
      const res = await api.post<{
        success: boolean
        plan?: Record<string, unknown>
      }>('/api/disguise-routing/simulate', {
        model: simModel.trim(),
        effort: simEffort,
        session_id: `sim_${Date.now()}`,
        force_argon: simForceArgon,
      })
      if (res.plan) {
        const p = res.plan
        setSimOutput(
          `命中规则: ${String(p.matched_rule_name)} (${String(p.matched_rule_id)}) | 选定目标池: ${String(p.selected_pool_id)} | 上游模型: ${String(p.upstream_model)} (effort=${String(p.upstream_effort)}) | 伪装开启: ${p.disguise_enabled ? '是' : '否 (CPA1 原生直连)'}`
        )
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '仿真失败')
    }
  }

  if (!isRoot) return null

  const anyArgonRuleEnabled = Boolean(
    config?.rules.some(
      (r) =>
        r.enabled &&
        r.targets.some((t) => t.pool_id === 'cpa2' && t.weight > 0)
    )
  )

  const maxBucketPct = Math.max(
    1,
    ...activePreview.buckets.map((b) => b.pct)
  )

  return (
    <div
      id='disguise-routing-console'
      className='space-y-5 rounded-xl border border-primary/25 bg-card p-5 shadow-xs'
    >
      {/* Top Header Bar */}
      <div className='flex flex-wrap items-center justify-between gap-3 border-b pb-4'>
        <div>
          <div className='flex flex-wrap items-center gap-2'>
            <h3 className='text-base font-semibold'>
              首字响应延迟伪装 (TTFT) 与上游路由策略
            </h3>
            <Badge variant='default' className='bg-emerald-600 text-white'>
              三重数学分布叠加
            </Badge>
            <Badge
              variant='outline'
              className={
                anyArgonRuleEnabled
                  ? 'border-amber-500/40 bg-amber-500/10 text-amber-600'
                  : 'border-emerald-500/40 bg-emerald-500/10 text-emerald-600'
              }
            >
              {anyArgonRuleEnabled
                ? '已开启 Argon 混池分流'
                : '全模型 100% 直连 CPA1 (Argon 伪装已关闭)'}
            </Badge>
          </div>
          <p className='mt-1 text-xs text-muted-foreground'>
            无需理解复杂的概率公式，直接通过下方滑块设定「最快值 / 最常用核心值 / 主要上限 / 长尾范围」，系统自动叠加三种概率分布生成自然随机的首字响应时间，并内置上游空响应自动重试自愈。
          </p>
        </div>

        <div className='flex flex-wrap items-center gap-2'>
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={() => void loadConfig()}
            disabled={loading || saving}
          >
            刷新
          </Button>
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={() => void handleReset()}
            disabled={loading || saving}
          >
            恢复推荐默认
          </Button>
          <Button
            type='button'
            size='sm'
            onClick={() => void handleSave()}
            disabled={loading || saving || !config}
          >
            {saving ? '保存同步中...' : '保存并全节点热生效'}
          </Button>
        </div>
      </div>

      {!config ? (
        <div className='py-6 text-center text-sm text-muted-foreground'>
          {loading ? '正在读取配置...' : '未能连接本地 Rust 加速器配置接口'}
        </div>
      ) : (
        <>
          {/* PART 1: Intuitive TTFT 3-Distribution Superposition Tuner + Live Chart */}
          <div className='rounded-xl border bg-muted/15 p-4 space-y-4'>
            <div className='flex flex-wrap items-center justify-between gap-2'>
              <div className='flex items-center gap-3'>
                <span className='text-sm font-semibold'>
                  🎲 1. 伪装首字响应时间 (TTFT) 直观调节器
                </span>
                <label className='flex items-center gap-1.5 text-xs font-medium cursor-pointer'>
                  <input
                    type='checkbox'
                    checked={ttft.enabled}
                    onChange={(e) => updateTtftField('enabled', e.target.checked)}
                  />
                  启用首字延迟伪装
                </label>
              </div>

              <div className='flex flex-wrap items-center gap-1.5'>
                <span className='text-xs text-muted-foreground mr-1'>一键预设：</span>
                <Button
                  type='button'
                  size='sm'
                  variant={ttft.preset === 'fast_core' ? 'default' : 'outline'}
                  className='h-7 text-xs'
                  onClick={() => applyTtftPreset('fast_core')}
                >
                  ⚡ 推荐默认 (核心 2.5s · 85% &lt; 4s)
                </Button>
                <Button
                  type='button'
                  size='sm'
                  variant={ttft.preset === 'snappy' ? 'default' : 'outline'}
                  className='h-7 text-xs'
                  onClick={() => applyTtftPreset('snappy')}
                >
                  🚀 极速响应 (核心 1.9s)
                </Button>
                <Button
                  type='button'
                  size='sm'
                  variant={ttft.preset === 'deep_think' ? 'default' : 'outline'}
                  className='h-7 text-xs'
                  onClick={() => applyTtftPreset('deep_think')}
                >
                  🧠 深度思考感 (核心 3.2s)
                </Button>
              </div>
            </div>

            <div className='grid grid-cols-1 gap-5 lg:grid-cols-12'>
              {/* Left Column: Plain-Language Sliders */}
              <div className='space-y-3 lg:col-span-6'>
                {/* Slider 1: Fastest Boundary (min_ms) */}
                <div className='rounded-lg border bg-background p-3'>
                  <div className='flex items-center justify-between text-xs font-medium'>
                    <span>① 最快响应边界（绝不快于此值）</span>
                    <span className='font-mono text-primary font-semibold'>
                      {(ttft.min_ms / 1000).toFixed(2)} 秒 ({ttft.min_ms} ms)
                    </span>
                  </div>
                  <div className='mt-2 flex items-center gap-3'>
                    <input
                      type='range'
                      min={500}
                      max={3500}
                      step={50}
                      value={ttft.min_ms}
                      onChange={(e) => updateTtftField('min_ms', Number(e.target.value))}
                      className='w-full accent-primary'
                    />
                    <Input
                      type='number'
                      value={ttft.min_ms}
                      onChange={(e) => updateTtftField('min_ms', Number(e.target.value))}
                      className='h-7 w-24 text-xs font-mono'
                    />
                  </div>
                  <p className='mt-1 text-[11px] text-muted-foreground'>
                    防止首字瞬间蹦出暴露转发特征，推荐设为 <strong>1200 ms (1.2秒)</strong>。
                  </p>
                </div>

                {/* Slider 2: Core Peak Value (core_ms) */}
                <div className='rounded-lg border border-emerald-500/30 bg-background p-3'>
                  <div className='flex items-center justify-between text-xs font-medium'>
                    <span>② 最期望核心常用值（出现最密集的黄金时间）</span>
                    <span className='font-mono text-emerald-600 font-semibold'>
                      {(ttft.core_ms / 1000).toFixed(2)} 秒 ({ttft.core_ms} ms)
                    </span>
                  </div>
                  <div className='mt-2 flex items-center gap-3'>
                    <input
                      type='range'
                      min={1400}
                      max={5500}
                      step={50}
                      value={ttft.core_ms}
                      onChange={(e) => updateTtftField('core_ms', Number(e.target.value))}
                      className='w-full accent-emerald-600'
                    />
                    <Input
                      type='number'
                      value={ttft.core_ms}
                      onChange={(e) => updateTtftField('core_ms', Number(e.target.value))}
                      className='h-7 w-24 text-xs font-mono'
                    />
                  </div>
                  <p className='mt-1 text-[11px] text-muted-foreground'>
                    日常调用最常感受到的首字速度，推荐设为 <strong>2200 ~ 2800 ms (默认 2.5秒)</strong>。
                  </p>
                </div>

                {/* Slider 3: Primary Upper Bound (primary_upper_ms) */}
                <div className='rounded-lg border bg-background p-3'>
                  <div className='flex items-center justify-between text-xs font-medium'>
                    <span>③ 日常主要上限（80% 以上请求在此时间内返回）</span>
                    <span className='font-mono text-primary font-semibold'>
                      {(ttft.primary_upper_ms / 1000).toFixed(2)} 秒 ({ttft.primary_upper_ms} ms)
                    </span>
                  </div>
                  <div className='mt-2 flex items-center gap-3'>
                    <input
                      type='range'
                      min={2500}
                      max={7500}
                      step={100}
                      value={ttft.primary_upper_ms}
                      onChange={(e) =>
                        updateTtftField('primary_upper_ms', Number(e.target.value))
                      }
                      className='w-full accent-primary'
                    />
                    <Input
                      type='number'
                      value={ttft.primary_upper_ms}
                      onChange={(e) =>
                        updateTtftField('primary_upper_ms', Number(e.target.value))
                      }
                      className='h-7 w-24 text-xs font-mono'
                    />
                  </div>
                  <p className='mt-1 text-[11px] text-muted-foreground'>
                    控制主力分布区的右边界，推荐设为 <strong>4000 ms (4.0秒)</strong>。
                  </p>
                </div>

                {/* Slider 4: Normal Max + Long Tail Range */}
                <div className='rounded-lg border bg-background p-3 space-y-2'>
                  <div className='flex items-center justify-between text-xs font-medium'>
                    <span>④ 常规最慢值 &amp; 偶发长尾散步范围</span>
                    <span className='font-mono text-purple-600 font-semibold'>
                      常规 ≤ {(ttft.normal_max_ms / 1000).toFixed(1)}s · 偶发 {ttft.tail_probability_pct}% 散步至 {(ttft.tail_max_ms / 1000).toFixed(1)}s
                    </span>
                  </div>
                  <div className='grid grid-cols-3 gap-2'>
                    <div>
                      <label className='text-[11px] text-muted-foreground'>
                        常规最慢值 (ms)
                      </label>
                      <Input
                        type='number'
                        step={200}
                        value={ttft.normal_max_ms}
                        onChange={(e) =>
                          updateTtftField('normal_max_ms', Number(e.target.value))
                        }
                        className='h-7 text-xs font-mono'
                      />
                    </div>
                    <div>
                      <label className='text-[11px] text-muted-foreground'>
                        偶发长尾最慢 (ms)
                      </label>
                      <Input
                        type='number'
                        step={500}
                        value={ttft.tail_max_ms}
                        onChange={(e) =>
                          updateTtftField('tail_max_ms', Number(e.target.value))
                        }
                        className='h-7 text-xs font-mono'
                      />
                    </div>
                    <div>
                      <label className='text-[11px] text-muted-foreground'>
                        偶发长尾比例 (%)
                      </label>
                      <Input
                        type='number'
                        min={0}
                        max={30}
                        value={ttft.tail_probability_pct}
                        onChange={(e) =>
                          updateTtftField(
                            'tail_probability_pct',
                            Number(e.target.value)
                          )
                        }
                        className='h-7 text-xs font-mono'
                      />
                    </div>
                  </div>
                </div>

                {/* Slider 5: Spread Randomness */}
                <div className='rounded-lg border bg-background p-3'>
                  <div className='flex items-center justify-between text-xs font-medium'>
                    <span>⑤ 离散随机度（三种数学分布随机混合强度）</span>
                    <span className='font-mono text-primary font-semibold'>
                      {ttft.spread_randomness}%
                    </span>
                  </div>
                  <div className='mt-2 flex items-center gap-3'>
                    <input
                      type='range'
                      min={10}
                      max={100}
                      step={2}
                      value={ttft.spread_randomness}
                      onChange={(e) =>
                        updateTtftField('spread_randomness', Number(e.target.value))
                      }
                      className='w-full accent-primary'
                    />
                  </div>
                  <p className='mt-1 text-[11px] text-muted-foreground'>
                    自动按随机权重混合「偏移对数正态 + 五阶伽马 + 威布尔」三种分布并注入微秒级抖动，数值越高越难被逆向统计。
                  </p>
                </div>
              </div>

              {/* Right Column: Live Distribution Histogram & Summary Cards */}
              <div className='flex flex-col justify-between space-y-3 lg:col-span-6'>
                {/* 4 Key Summary Metrics */}
                <div className='grid grid-cols-2 gap-2.5 sm:grid-cols-4'>
                  <div className='rounded-lg border bg-background p-2.5'>
                    <div className='text-[11px] text-muted-foreground'>
                      &lt; {(ttft.primary_upper_ms / 1000).toFixed(1)}s 主力占比
                    </div>
                    <div className='mt-0.5 text-base font-bold text-emerald-600'>
                      {activePreview.summary.share_below_primary_upper_pct}%
                    </div>
                  </div>
                  <div className='rounded-lg border bg-background p-2.5'>
                    <div className='text-[11px] text-muted-foreground'>
                      最密集核心区
                    </div>
                    <div className='mt-0.5 text-base font-bold text-primary'>
                      {activePreview.summary.peak_range_label}
                    </div>
                  </div>
                  <div className='rounded-lg border bg-background p-2.5'>
                    <div className='text-[11px] text-muted-foreground'>
                      中位数 P50 / 均值
                    </div>
                    <div className='mt-0.5 text-sm font-bold'>
                      {(activePreview.summary.p50_ms / 1000).toFixed(2)}s /{' '}
                      {(activePreview.summary.mean_ms / 1000).toFixed(2)}s
                    </div>
                  </div>
                  <div className='rounded-lg border bg-background p-2.5'>
                    <div className='text-[11px] text-muted-foreground'>
                      偶发长尾 (&gt;{(ttft.normal_max_ms / 1000).toFixed(1)}s)
                    </div>
                    <div className='mt-0.5 text-base font-bold text-purple-600'>
                      {activePreview.summary.share_in_tail_pct}%
                    </div>
                  </div>
                </div>

                {/* Live Bar Chart */}
                <div className='rounded-lg border bg-background p-3'>
                  <div className='mb-2 flex flex-wrap items-center justify-between gap-2 text-xs'>
                    <span className='font-medium'>
                      📊 实时概率分布预览图 (1200 次请求仿真统计)
                    </span>
                    <div className='flex items-center gap-2'>
                      <span className='inline-flex items-center gap-1 text-[11px] text-muted-foreground'>
                        <span className='inline-block h-2 w-2 rounded-xs bg-emerald-500' />
                        核心峰值
                      </span>
                      <span className='inline-flex items-center gap-1 text-[11px] text-muted-foreground'>
                        <span className='inline-block h-2 w-2 rounded-xs bg-blue-500' />
                        日常主区间
                      </span>
                      <span className='inline-flex items-center gap-1 text-[11px] text-muted-foreground'>
                        <span className='inline-block h-2 w-2 rounded-xs bg-amber-500' />
                        4-6s过渡
                      </span>
                      <span className='inline-flex items-center gap-1 text-[11px] text-muted-foreground'>
                        <span className='inline-block h-2 w-2 rounded-xs bg-purple-500' />
                        长尾区
                      </span>
                      <Button
                        type='button'
                        size='sm'
                        variant='outline'
                        className='h-6 px-2 text-[11px]'
                        onClick={() => void fetchBackendTtftPreview()}
                      >
                        🎲 重新随机抽样
                      </Button>
                    </div>
                  </div>

                  <div className='flex h-44 items-end gap-1 pt-4 pb-1 px-1 border-b'>
                    {activePreview.buckets.map((b, idx) => {
                      const heightPct = Math.max(
                        3,
                        Math.round((b.pct / maxBucketPct) * 100)
                      )
                      const barColor =
                        b.zone === 'core'
                          ? 'bg-emerald-500'
                          : b.zone === 'tail'
                            ? 'bg-purple-500'
                            : b.zone === 'secondary'
                              ? 'bg-amber-500'
                              : 'bg-blue-500'
                      return (
                        <div
                          key={idx}
                          className='group relative flex h-full flex-1 flex-col items-center justify-end'
                          title={`${b.label}: ${b.pct}% (${b.count}/1200)`}
                        >
                          <span className='mb-0.5 text-[9px] text-muted-foreground'>
                            {b.pct >= 4 ? `${b.pct}%` : ''}
                          </span>
                          <div
                            className={`w-full rounded-t-xs transition-all ${barColor}`}
                            style={{ height: `${heightPct}%` }}
                          />
                        </div>
                      )
                    })}
                  </div>
                  <div className='mt-1.5 flex justify-between text-[10px] font-mono text-muted-foreground'>
                    <span>{(ttft.min_ms / 1000).toFixed(1)}s (最快)</span>
                    <span>{(ttft.core_ms / 1000).toFixed(1)}s (核心峰值)</span>
                    <span>{(ttft.primary_upper_ms / 1000).toFixed(1)}s (主上限)</span>
                    <span>{(ttft.normal_max_ms / 1000).toFixed(1)}s (常规上限)</span>
                    <span>{(ttft.tail_max_ms / 1000).toFixed(1)}s (长尾上限)</span>
                  </div>
                </div>

                {/* Live Sample Sequence Preview */}
                <div className='rounded-lg border bg-background p-3'>
                  <div className='text-xs font-medium mb-1'>
                    最近 12 次连续请求的首字延迟抽样序列（含反聚簇排斥与微秒随机抖动）：
                  </div>
                  <div className='flex flex-wrap gap-1.5 font-mono text-xs'>
                    {activePreview.sample_series_ms.slice(0, 12).map((ms, idx) => (
                      <span
                        key={idx}
                        className={`rounded border px-2 py-0.5 ${
                          ms > ttft.normal_max_ms
                            ? 'border-purple-500/40 bg-purple-500/10 text-purple-600'
                            : Math.abs(ms - ttft.core_ms) <= 450
                              ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-600 font-semibold'
                              : 'bg-muted/40'
                        }`}
                      >
                        {(ms / 1000).toFixed(3)}s
                      </span>
                    ))}
                  </div>
                </div>
              </div>
            </div>
          </div>

          {/* PART 2: Clean, Direct Upstream Routing & Argon Disguise Switch */}
          <div className='rounded-xl border bg-muted/15 p-4 space-y-3'>
            <div className='flex flex-wrap items-center justify-between gap-2'>
              <div>
                <h4 className='text-sm font-semibold'>
                  🛡️ 2. 模型路由与 Argon 伪装策略（简洁模式）
                </h4>
                <p className='text-xs text-muted-foreground'>
                  当前 <code>astra</code> 与 <code>sol</code> 在 CPA1 主池比 Argon 更具性价比，因此已默认关闭 Argon 伪装，所有模型 100% 走 CPA1 原生主池。若日后需要开启 Argon 分流，直接打开对应规则开关即可。
                </p>
              </div>
              <Button
                type='button'
                size='sm'
                variant='outline'
                className='h-7 text-xs'
                onClick={() => {
                  setShowAdvanced((v) => !v)
                  if (!showAdvanced) void loadSamples()
                }}
              >
                {showAdvanced ? '收起高级设置与诊断工具 ▲' : '🛠️ 展开高级设置与路由诊断工具 ▼'}
              </Button>
            </div>

            <div className='space-y-2.5'>
              {config.rules.map((rule, rIdx) => {
                const cpa2Target = rule.targets.find((t) => t.pool_id === 'cpa2')
                const isArgonRule = Boolean(cpa2Target)

                return (
                  <div
                    key={rule.id}
                    className={`rounded-lg border p-3 transition-colors ${
                      rule.enabled
                        ? 'border-emerald-500/35 bg-background'
                        : 'border-dashed bg-muted/20 opacity-85'
                    }`}
                  >
                    <div className='flex flex-wrap items-center justify-between gap-3'>
                      <div className='space-y-1'>
                        <div className='flex flex-wrap items-center gap-2'>
                          <span className='text-sm font-semibold'>{rule.name}</span>
                          {rule.enabled ? (
                            <Badge className='bg-emerald-600 text-white'>
                              已启用
                            </Badge>
                          ) : (
                            <Badge variant='secondary'>
                              默认已关闭（不走 Argon）
                            </Badge>
                          )}
                          <span className='text-xs font-mono text-muted-foreground'>
                            适用模型: {rule.model_patterns.join(', ')}
                          </span>
                        </div>
                        <p className='text-xs text-muted-foreground'>{rule.notes}</p>
                      </div>

                      <div className='flex items-center gap-4'>
                        {isArgonRule && rule.enabled && rule.mode === 'weighted_mix' && (
                          <div className='flex items-center gap-2 text-xs'>
                            <span className='text-muted-foreground'>Argon 占比:</span>
                            <input
                              type='range'
                              min={0}
                              max={100}
                              step={5}
                              value={cpa2Target?.weight ?? 0}
                              onChange={(e) =>
                                updateMixArgonWeight(rIdx, Number(e.target.value))
                              }
                              className='w-28 accent-amber-500'
                            />
                            <span className='font-mono font-semibold'>
                              {cpa2Target?.weight ?? 0}%
                            </span>
                          </div>
                        )}

                        <label className='flex items-center gap-2 text-xs font-medium cursor-pointer select-none'>
                          <input
                            type='checkbox'
                            checked={rule.enabled}
                            onChange={(e) => toggleRuleEnabled(rIdx, e.target.checked)}
                          />
                          {rule.enabled ? '启用中' : '点击开启'}
                        </label>
                      </div>
                    </div>
                  </div>
                )
              })}
            </div>
          </div>

          {/* PART 3: Collapsible Advanced Diagnostics & Pool Settings */}
          {showAdvanced && (
            <div className='rounded-xl border border-dashed bg-muted/10 p-4 space-y-4'>
              <div className='text-xs font-semibold text-muted-foreground'>
                🛠️ 高级路由仿真测试器与最近捕获样本（仅供排障使用）
              </div>

              <div className='flex flex-wrap items-end gap-2 rounded-lg border bg-background p-3'>
                <div>
                  <label className='text-[11px] text-muted-foreground'>测试模型名</label>
                  <Input
                    value={simModel}
                    onChange={(e) => setSimModel(e.target.value)}
                    className='h-8 w-44 text-xs font-mono'
                  />
                </div>
                <div>
                  <label className='text-[11px] text-muted-foreground'>思考强度</label>
                  <select
                    className='flex h-8 rounded-md border border-input bg-background px-2 text-xs'
                    value={simEffort}
                    onChange={(e) => setSimEffort(e.target.value)}
                  >
                    {['low', 'medium', 'high', 'xhigh', 'max'].map((ef) => (
                      <option key={ef} value={ef}>
                        {ef}
                      </option>
                    ))}
                  </select>
                </div>
                <label className='flex items-center gap-1.5 text-xs pb-1.5'>
                  <input
                    type='checkbox'
                    checked={simForceArgon}
                    onChange={(e) => setSimForceArgon(e.target.checked)}
                  />
                  携带 X-Force-Argon 头
                </label>
                <Button
                  type='button'
                  size='sm'
                  variant='secondary'
                  onClick={() => void runSimulation()}
                >
                  测试路由命中结果
                </Button>
                {simOutput && (
                  <div className='w-full mt-2 rounded border bg-muted/30 p-2 font-mono text-xs'>
                    {simOutput}
                  </div>
                )}
              </div>

              <div className='rounded-lg border bg-background p-3 space-y-2'>
                <div className='flex items-center justify-between'>
                  <span className='text-xs font-medium'>
                    最近捕获请求样本 ({samples.length} 条)
                  </span>
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    className='h-7 text-xs'
                    onClick={() => void loadSamples()}
                  >
                    刷新样本
                  </Button>
                </div>
                {samples.length === 0 ? (
                  <div className='text-xs text-muted-foreground py-2'>
                    暂无捕获样本
                  </div>
                ) : (
                  <div className='max-h-56 overflow-auto'>
                    <table className='w-full text-xs'>
                      <thead>
                        <tr className='border-b text-left text-muted-foreground'>
                          <th className='p-1.5'>时间</th>
                          <th className='p-1.5'>请求模型</th>
                          <th className='p-1.5'>实际路由目标池 &amp; 上游模型</th>
                          <th className='p-1.5'>操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {samples.map((s, idx) => (
                          <tr key={s.id} className='border-b'>
                            <td className='p-1.5'>
                              {new Date(s.captured_at_unix * 1000).toLocaleTimeString()}
                            </td>
                            <td className='p-1.5 font-mono'>
                              {s.downstream_model} ({s.downstream_effort})
                            </td>
                            <td className='p-1.5 font-mono'>
                              {s.selected_pool_id} ➔ {s.upstream_model}
                            </td>
                            <td className='p-1.5'>
                              <Button
                                type='button'
                                size='sm'
                                variant='outline'
                                className='h-6 px-2 text-[11px]'
                                onClick={() =>
                                  setSelectedSampleIdx(
                                    selectedSampleIdx === idx ? null : idx
                                  )
                                }
                              >
                                {selectedSampleIdx === idx ? '收起' : '查看载荷'}
                              </Button>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
                {selectedSampleIdx !== null && samples[selectedSampleIdx] && (
                  <pre className='max-h-48 overflow-auto rounded border bg-muted/40 p-2 font-mono text-[11px] whitespace-pre-wrap'>
                    {samples[selectedSampleIdx].rewritten_body_preview}
                  </pre>
                )}
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}
