/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
import {
  Component,
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ErrorInfo,
  type ReactNode,
} from 'react'
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
  max_samples_per_model?: number
  max_total_samples?: number
  ttl_seconds?: number
  max_body_preview_bytes?: number
  redact_auth_headers?: boolean
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
  global_enabled?: boolean
  force_disguise_headers?: string[]
  force_disguise_model_suffixes?: string[]
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

function computeLocalTtftPreview(
  raw: TtftDistributionConfig,
  seedOffset: number
): TtftPreviewData {
  const min_ms = Math.max(300, Math.min(8000, Number(raw.min_ms) || 1200))
  const core_ms = Math.max(
    min_ms + 300,
    Math.min(12000, Number(raw.core_ms) || 2500)
  )
  const primary_upper_ms = Math.max(
    core_ms + 400,
    Math.min(16000, Number(raw.primary_upper_ms) || 4000)
  )
  const normal_max_ms = Math.max(
    primary_upper_ms + 500,
    Math.min(25000, Number(raw.normal_max_ms) || 6000)
  )
  const tail_max_ms = Math.max(
    normal_max_ms + 1000,
    Math.min(45000, Number(raw.tail_max_ms) || 12000)
  )
  const tail_probability_pct = Math.max(
    0,
    Math.min(35, Number(raw.tail_probability_pct) ?? 5)
  )
  const spread_randomness = Math.max(
    5,
    Math.min(100, Number(raw.spread_randomness) ?? 68)
  )

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

    const sigma1 = 0.18 + 0.24 * spread
    const mu1 = Math.log(deltaCore) + 0.45 * sigma1 * sigma1
    const d1 = min_ms + Math.exp(mu1 + sigma1 * randNormal())

    const theta2 = (deltaCore * (0.94 + 0.14 * spread)) / 4.25
    const d2 = min_ms + theta2 * randErlang(5)

    const k3 = Math.max(1.45, 2.75 - 1.05 * spread)
    const scale3 = deltaCore / Math.pow(0.68, 1 / k3)
    const d3 =
      min_ms + scale3 * Math.pow(-Math.log(Math.max(1e-7, rand01())), 1 / k3)

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
      x =
        primary_upper_ms +
        secSpan * (1 - Math.exp(-over / (secSpan * (0.85 + 0.4 * spread))))
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
  const pctAt = (p: number) =>
    sorted[Math.min(n - 1, Math.max(0, Math.round((n - 1) * p)))]
  const mean = Math.round(sorted.reduce((a, b) => a + b, 0) / n)
  const belowPrimary = sorted.filter((v) => v <= primary_upper_ms).length
  const inCore = sorted.filter((v) => Math.abs(v - core_ms) <= 450).length
  const inNormal = sorted.filter((v) => v <= normal_max_ms).length
  const inTail = sorted.filter((v) => v > normal_max_ms).length

  const step = Math.max(
    250,
    Math.floor(Math.max(2400, tail_max_ms - min_ms) / 18)
  )
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

type ActiveTab = 'all' | 'ttft' | 'rules' | 'pools' | 'diag'

function unwrapApiPayload<T>(res: unknown): T {
  if (
    res &&
    typeof res === 'object' &&
    'data' in res &&
    (res as { data?: unknown }).data &&
    typeof (res as { data?: unknown }).data === 'object'
  ) {
    return (res as { data: T }).data
  }
  return res as T
}

function normalizeDisguiseConfig(
  raw: DisguiseRoutingConfig | undefined | null
): DisguiseRoutingConfig | null {
  if (
    !raw ||
    typeof raw !== 'object' ||
    !Array.isArray(raw.rules) ||
    !Array.isArray(raw.pools)
  ) {
    return null
  }
  const loadedTtft: TtftDistributionConfig = {
    ...DEFAULT_TTFT_CONFIG,
    ...(raw.ttft ?? raw.ttft_distribution ?? {}),
  }
  return {
    ...raw,
    pools: raw.pools.map((p) => ({
      id: p?.id ?? 'cpa1',
      name: p?.name ?? '',
      base_url: p?.base_url ?? '',
      api_key: p?.api_key ?? '',
      enabled: Boolean(p?.enabled),
      description: p?.description ?? '',
    })),
    rules: raw.rules.map((r) => ({
      ...r,
      id: r?.id ?? `rule-${Math.random().toString(36).slice(2, 7)}`,
      name: r?.name ?? '',
      enabled: Boolean(r?.enabled),
      priority: Number(r?.priority) || 10,
      model_patterns: Array.isArray(r?.model_patterns) ? r.model_patterns : [],
      mode: r?.mode ?? 'weighted_mix',
      sticky_session: {
        enabled: Boolean(r?.sticky_session?.enabled ?? true),
        min_soft_turns: Number(r?.sticky_session?.min_soft_turns) || 5,
        max_soft_turns: Number(r?.sticky_session?.max_soft_turns) || 9,
        ttl_seconds: Number(r?.sticky_session?.ttl_seconds) || 240,
        failover_on_error: Boolean(
          r?.sticky_session?.failover_on_error ?? true
        ),
      },
      targets: Array.isArray(r?.targets)
        ? r.targets.map((t) => ({
            ...t,
            target_id: t?.target_id ?? 'target',
            pool_id: t?.pool_id ?? 'cpa1',
            weight: Number(t?.weight) || 0,
            upstream_model: t?.upstream_model ?? '',
            disguise_enabled: Boolean(t?.disguise_enabled),
            protocol_family: t?.protocol_family ?? 'openai_native',
            masquerade_model: t?.masquerade_model ?? '$clean_model',
            masquerade_provider: t?.masquerade_provider ?? 'OpenAI',
            inject_identity_prompt: Boolean(t?.inject_identity_prompt),
            scrub_response_identity: Boolean(t?.scrub_response_identity),
            effort_map: Array.isArray(t?.effort_map)
              ? t.effort_map.map((em) => ({
                  ...em,
                  downstream_efforts: Array.isArray(em?.downstream_efforts)
                    ? em.downstream_efforts
                    : [],
                  upstream_model_override: em?.upstream_model_override ?? '',
                  upstream_effort: em?.upstream_effort ?? 'high',
                  thinking_budget:
                    em?.thinking_budget === undefined
                      ? null
                      : em.thinking_budget,
                }))
              : [],
          }))
        : [],
      notes: r?.notes ?? '',
    })),
    ttft: loadedTtft,
    ttft_distribution: loadedTtft,
  }
}

class DisguiseConsoleErrorBoundary extends Component<
  { children: ReactNode },
  { error: Error | null }
> {
  constructor(props: { children: ReactNode }) {
    super(props)
    this.state = { error: null }
  }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  override componentDidCatch(error: Error, info: ErrorInfo) {
    // eslint-disable-next-line no-console
    console.error('[DisguiseRoutingSection] render error:', error, info)
  }

  override render() {
    if (this.state.error) {
      return (
        <div className='rounded-xl border border-destructive/40 bg-card p-5 space-y-3'>
          <div className='text-sm font-semibold text-destructive'>
            伪装与混池控制台渲染异常：{this.state.error.message}
          </div>
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={() => this.setState({ error: null })}
          >
            重新加载控制台
          </Button>
        </div>
      )
    }
    return this.props.children
  }
}

export function DisguiseRoutingSection() {
  return (
    <DisguiseConsoleErrorBoundary>
      <DisguiseRoutingSectionInner />
    </DisguiseConsoleErrorBoundary>
  )
}

function DisguiseRoutingSectionInner() {
  const userRole = useAuthStore((s) => s.auth.user?.role ?? ROLE.ADMIN)
  const isAdmin = userRole >= ROLE.ADMIN

  const [config, setConfig] = useState<DisguiseRoutingConfig | null>(null)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [activeTab, setActiveTab] = useState<ActiveTab>('all')
  const [expandedRuleIds, setExpandedRuleIds] = useState<Record<string, boolean>>({
    'rule-sol-terra-weighted-mix': true,
    'rule-force-disguise-suffix': false,
  })
  const [previewSeed, setPreviewSeed] = useState(1)
  const [serverPreview, setServerPreview] = useState<TtftPreviewData | null>(
    null
  )

  // Simulation & Captured Samples
  const [simModel, setSimModel] = useState('gpt-6.1-sol')
  const [simEffort, setSimEffort] = useState('xhigh')
  const [simForceArgon, setSimForceArgon] = useState(false)
  const [simOutput, setSimOutput] = useState<string>('')
  const [samples, setSamples] = useState<CapturedSample[]>([])
  const [selectedSampleIdx, setSelectedSampleIdx] = useState<number | null>(
    null
  )

  const loadConfig = useCallback(async () => {
    setLoading(true)
    try {
      const res = await api.get<{
        success: boolean
        config?: DisguiseRoutingConfig
        message?: string
      }>('/api/disguise-routing/config')
      const payload = unwrapApiPayload<{
        success?: boolean
        config?: DisguiseRoutingConfig
        message?: string
      }>(res)
      const normalized = normalizeDisguiseConfig(payload?.config)
      if (normalized) {
        setConfig(normalized)
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
      const payload = unwrapApiPayload<{
        success?: boolean
        samples?: CapturedSample[]
      }>(res)
      setSamples(Array.isArray(payload?.samples) ? payload.samples : [])
    } catch {
      // ignore
    }
  }, [])

  useEffect(() => {
    if (isAdmin) {
      void loadConfig()
      void loadSamples()
    }
  }, [isAdmin, loadConfig, loadSamples])

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
    toast.success('已套用 TTFT 分布预设，点击右上角「保存并全节点热生效」即可应用')
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
      const payload = unwrapApiPayload<TtftPreviewData & { success?: boolean }>(
        res
      )
      if (payload && payload.summary && Array.isArray(payload.buckets)) {
        setServerPreview(payload)
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
      const payload = unwrapApiPayload<{
        success?: boolean
        config?: DisguiseRoutingConfig
        message?: string
      }>(res)
      const normalized = normalizeDisguiseConfig(payload?.config)
      if (!payload?.success || !normalized) {
        toast.error(payload?.message || '保存失败')
        return
      }
      setConfig(normalized)
      toast.success('配置已保存并同步至本地 Rust 加速器与 PostgreSQL 数据库')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  const handleReset = async () => {
    if (
      !window.confirm(
        '确定恢复推荐默认设置（TTFT 核心 2.5s 三分布叠加 + 默认不开启混池分流，保留全部伪装配置）吗？'
      )
    ) {
      return
    }
    setSaving(true)
    try {
      const res = await api.post<{
        success: boolean
        config?: DisguiseRoutingConfig
      }>('/api/disguise-routing/reset')
      const payload = unwrapApiPayload<{
        success?: boolean
        config?: DisguiseRoutingConfig
      }>(res)
      const normalized = normalizeDisguiseConfig(payload?.config)
      if (normalized) {
        setConfig(normalized)
        setServerPreview(null)
        toast.success('已恢复推荐默认配置')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '重置失败')
    } finally {
      setSaving(false)
    }
  }

  const updateRule = (
    ruleIdx: number,
    updater: (r: ModelRoutingRule) => ModelRoutingRule
  ) => {
    if (!config) return
    const nextRules = (config.rules ?? []).map((r, i) =>
      i === ruleIdx ? updater(r) : r
    )
    setConfig({ ...config, rules: nextRules })
  }

  const addNewRule = (
    presetType: 'cpa1_disguise' | 'cpa2_forced' | 'weighted_mix'
  ) => {
    if (!config) return
    const newId = `rule-custom-${Date.now().toString().slice(-4)}`
    let newRule: ModelRoutingRule
    if (presetType === 'cpa1_disguise') {
      newRule = {
        id: newId,
        name: 'CPA1 内部跨模型伪装规则 (如用 gpt-5.6-sol 伪装目标模型)',
        enabled: true,
        priority: 15,
        model_patterns: ['gpt-6.1-custom'],
        mode: 'forced_disguise',
        sticky_session: {
          enabled: true,
          min_soft_turns: 6,
          max_soft_turns: 10,
          ttl_seconds: 240,
          failover_on_error: true,
        },
        targets: [
          {
            target_id: 'cpa1-internal-disguise',
            pool_id: 'cpa1',
            weight: 100,
            upstream_model: 'gpt-5.6-sol',
            disguise_enabled: true,
            protocol_family: 'openai_native',
            masquerade_model: '$clean_model',
            masquerade_provider: 'OpenAI',
            inject_identity_prompt: true,
            scrub_response_identity: true,
            effort_map: [
              {
                downstream_efforts: ['low', 'medium', 'high', 'xhigh', 'max'],
                upstream_model_override: 'gpt-5.6-sol',
                upstream_effort: 'high',
                thinking_budget: null,
              },
            ],
          },
        ],
        notes:
          '在 CPA1 主池内部将下游模型映射到指定上游模型，并自动注入身份提示词与还原响应模型名。',
      }
    } else if (presetType === 'cpa2_forced') {
      newRule = {
        id: newId,
        name: 'CPA2 Argon 100% 强制伪装规则',
        enabled: false,
        priority: 16,
        model_patterns: ['gpt-6.1-sol-argon'],
        mode: 'forced_disguise',
        sticky_session: {
          enabled: true,
          min_soft_turns: 6,
          max_soft_turns: 10,
          ttl_seconds: 240,
          failover_on_error: true,
        },
        targets: [
          {
            target_id: 'cpa2-argon-disguise',
            pool_id: 'cpa2',
            weight: 100,
            upstream_model: 'gemini-4-argon-high',
            disguise_enabled: true,
            protocol_family: 'argon_cpa',
            masquerade_model: '$clean_model',
            masquerade_provider: 'OpenAI',
            inject_identity_prompt: true,
            scrub_response_identity: true,
            effort_map: [
              {
                downstream_efforts: ['low'],
                upstream_model_override: 'gemini-4-argon-high(4096)',
                upstream_effort: 'low',
                thinking_budget: 4096,
              },
              {
                downstream_efforts: ['medium'],
                upstream_model_override: 'gemini-4-argon-high(12288)',
                upstream_effort: 'medium',
                thinking_budget: 12288,
              },
              {
                downstream_efforts: ['high'],
                upstream_model_override: 'gemini-4-argon-high(28672)',
                upstream_effort: 'high',
                thinking_budget: 28672,
              },
              {
                downstream_efforts: ['xhigh', 'max'],
                upstream_model_override: 'gemini-4-argon-high(49152)',
                upstream_effort: 'high',
                thinking_budget: 49152,
              },
            ],
          },
        ],
        notes: '将匹配模型 100% 路由至 CPA2 Argon 并执行完整身份伪装。',
      }
    } else {
      newRule = {
        id: newId,
        name: '新建 CPA1 + CPA2 权重混池伪装规则',
        enabled: false,
        priority: 20,
        model_patterns: ['*-sol'],
        mode: 'weighted_mix',
        sticky_session: {
          enabled: true,
          min_soft_turns: 5,
          max_soft_turns: 9,
          ttl_seconds: 240,
          failover_on_error: true,
        },
        targets: [
          {
            target_id: 'main-cpa1',
            pool_id: 'cpa1',
            weight: 60,
            upstream_model: '$clean_model',
            disguise_enabled: false,
            protocol_family: 'openai_native',
            masquerade_model: '$clean_model',
            masquerade_provider: 'OpenAI',
            inject_identity_prompt: false,
            scrub_response_identity: true,
            effort_map: [],
          },
          {
            target_id: 'argon-cpa2',
            pool_id: 'cpa2',
            weight: 40,
            upstream_model: 'gemini-4-argon-high',
            disguise_enabled: true,
            protocol_family: 'argon_cpa',
            masquerade_model: '$clean_model',
            masquerade_provider: 'OpenAI',
            inject_identity_prompt: true,
            scrub_response_identity: true,
            effort_map: [],
          },
        ],
        notes: '按权重在 CPA1 原生主池与 CPA2 Argon 伪装池之间分流。',
      }
    }
    setExpandedRuleIds((prev) => ({ ...prev, [newId]: true }))
    setConfig({ ...config, rules: [newRule, ...(config.rules ?? [])] })
    toast.success('已添加新伪装规则，配置完成后请点击右上角「保存并全节点热生效」')
  }

  const removeRule = (ruleIdx: number) => {
    if (!config) return
    const nextRules = (config.rules ?? []).filter((_, i) => i !== ruleIdx)
    setConfig({ ...config, rules: nextRules })
  }

  const updatePool = (
    poolIdx: number,
    field: keyof UpstreamPoolConfig,
    val: string | boolean
  ) => {
    if (!config) return
    const nextPools = (config.pools ?? []).map((p, i) =>
      i === poolIdx ? { ...p, [field]: val } : p
    )
    setConfig({ ...config, pools: nextPools })
  }

  const runSimulation = async (batchCount = 1) => {
    try {
      if (batchCount <= 1) {
        const res = await api.post<{
          success: boolean
          plan?: Record<string, unknown>
        }>('/api/disguise-routing/simulate', {
          model: simModel.trim(),
          effort: simEffort,
          session_id: `sim_${Date.now()}`,
          force_argon: simForceArgon,
        })
        const payload = unwrapApiPayload<{
          success?: boolean
          plan?: Record<string, unknown>
        }>(res)
        if (payload?.plan) {
          const p = payload.plan
          setSimOutput(
            `命中规则: ${String(p.matched_rule_name)} (${String(p.matched_rule_id)}) | 选定目标池: ${String(p.selected_pool_id)} | 上游模型: ${String(p.upstream_model)} (effort=${String(p.upstream_effort)}${p.thinking_budget ? `, budget=${String(p.thinking_budget)}` : ''}) | 伪装开启: ${p.disguise_enabled ? '✅ 是 (身份注入+痕迹抹除)' : '⚡ 否 (CPA1 原生直连)'}`
          )
        }
      } else {
        const counts: Record<string, number> = {}
        for (let i = 0; i < batchCount; i++) {
          const res = await api.post<{
            success: boolean
            plan?: Record<string, unknown>
          }>('/api/disguise-routing/simulate', {
            model: simModel.trim(),
            effort: simEffort,
            session_id: `batch_${Date.now()}_${i}_${Math.random()}`,
            force_argon: simForceArgon,
          })
          const payload = unwrapApiPayload<{
            success?: boolean
            plan?: Record<string, unknown>
          }>(res)
          if (payload?.plan) {
            const k = `${String(payload.plan.selected_pool_id)} -> ${String(payload.plan.upstream_model)} (${payload.plan.disguise_enabled ? '伪装' : '原生'})`
            counts[k] = (counts[k] ?? 0) + 1
          }
        }
        const summary = Object.entries(counts)
          .map(
            ([k, c]) =>
              `${k}: ${c}/${batchCount} (${Math.round((c / batchCount) * 100)}%)`
          )
          .join('  |  ')
        setSimOutput(`📊 ${batchCount} 次独立新会话抽样统计: ${summary}`)
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '仿真失败')
    }
  }

  if (!isAdmin) return null

  const anyMixEnabled = Boolean(
    (config?.rules ?? []).some(
      (r) =>
        r?.enabled &&
        (r?.targets ?? []).some((t) => t?.pool_id === 'cpa2' && (t?.weight ?? 0) > 0)
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
      {/* Top Action & Status Bar */}
      <div className='flex flex-wrap items-center justify-between gap-3 border-b pb-4'>
        <div>
          <div className='flex flex-wrap items-center gap-2'>
            <h3 className='text-base font-semibold'>
              伪装与混池路由控制台（TTFT 三分布叠加 · 模型伪装 · 上游分流）
            </h3>
            <Badge variant='default' className='bg-emerald-600 text-white'>
              TTFT 三重数学分布已启用
            </Badge>
            <Badge
              variant='outline'
              className={
                anyMixEnabled
                  ? 'border-amber-500/40 bg-amber-500/10 text-amber-600'
                  : 'border-emerald-500/40 bg-emerald-500/10 text-emerald-600'
              }
            >
              {anyMixEnabled
                ? '已开启 CPA2 Argon 伪装/混池规则'
                : '混池功能默认未开启（全模型 100% 走 CPA1，可随时在下方开启伪装规则）'}
            </Badge>
          </div>
          <p className='mt-1 text-xs text-muted-foreground'>
            本页面仅对管理员可见（普通用户菜单中不展示）。支持零数学门槛调节 TTFT 首字延迟分布，同时完整保留全部模型伪装与混池规则配置（默认关闭混池开关，可直接在下方展开并一键启用）。
          </p>
        </div>

        <div className='flex flex-wrap items-center gap-2'>
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={() => {
              void loadConfig()
              void loadSamples()
            }}
            disabled={loading || saving}
          >
            刷新状态
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
            {saving ? '保存同步中...' : '💾 保存并全节点热生效'}
          </Button>
        </div>
      </div>

      {/* Clean Navigation Tabs */}
      <div className='flex flex-wrap items-center gap-2 border-b pb-3'>
        {(
          [
            { id: 'all', label: '📋 全部视图 (一屏总览)' },
            { id: 'ttft', label: '🎲 1. TTFT 首字延迟伪装调节器' },
            { id: 'rules', label: '🎭 2. 模型伪装与混池分流规则' },
            { id: 'pools', label: '🌐 3. 上游号池配置 (CPA1 / CPA2)' },
            { id: 'diag', label: '🔍 4. 路由仿真测试与请求抓包' },
          ] as const
        ).map((tab) => (
          <Button
            key={tab.id}
            type='button'
            size='sm'
            variant={activeTab === tab.id ? 'default' : 'outline'}
            className='h-8 text-xs'
            onClick={() => setActiveTab(tab.id)}
          >
            {tab.label}
          </Button>
        ))}
      </div>

      {!config ? (
        <div className='py-8 text-center text-sm text-muted-foreground'>
          {loading ? '正在加载伪装与路由配置...' : '未能连接本地 Rust 加速器配置接口'}
        </div>
      ) : (
        <div className='space-y-6'>
          {/* =====================================================================
              MODULE 1: TTFT 3-Mathematical-Distribution Superposition Tuner
             ===================================================================== */}
          {(activeTab === 'all' || activeTab === 'ttft') && (
            <div className='rounded-xl border bg-muted/15 p-4 space-y-4'>
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <div className='flex items-center gap-3'>
                  <span className='text-sm font-semibold'>
                    🎲 1. 首字响应延迟伪装 (TTFT 三重数学分布叠加调节器)
                  </span>
                  <label className='flex items-center gap-1.5 text-xs font-medium cursor-pointer'>
                    <input
                      type='checkbox'
                      checked={ttft.enabled}
                      onChange={(e) =>
                        updateTtftField('enabled', e.target.checked)
                      }
                    />
                    启用首字延迟伪装
                  </label>
                </div>

                <div className='flex flex-wrap items-center gap-1.5'>
                  <span className='text-xs text-muted-foreground mr-1'>
                    一键预设：
                  </span>
                  <Button
                    type='button'
                    size='sm'
                    variant={
                      ttft.preset === 'fast_core' ? 'default' : 'outline'
                    }
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
                    variant={
                      ttft.preset === 'deep_think' ? 'default' : 'outline'
                    }
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
                        onChange={(e) =>
                          updateTtftField('min_ms', Number(e.target.value))
                        }
                        className='w-full accent-primary'
                      />
                      <Input
                        type='number'
                        value={ttft.min_ms}
                        onChange={(e) =>
                          updateTtftField('min_ms', Number(e.target.value))
                        }
                        className='h-7 w-24 text-xs font-mono'
                      />
                    </div>
                    <p className='mt-1 text-[11px] text-muted-foreground'>
                      防止首字瞬间蹦出暴露转发特征，推荐设为{' '}
                      <strong>1200 ms (1.2秒)</strong>。
                    </p>
                  </div>

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
                        onChange={(e) =>
                          updateTtftField('core_ms', Number(e.target.value))
                        }
                        className='w-full accent-emerald-600'
                      />
                      <Input
                        type='number'
                        value={ttft.core_ms}
                        onChange={(e) =>
                          updateTtftField('core_ms', Number(e.target.value))
                        }
                        className='h-7 w-24 text-xs font-mono'
                      />
                    </div>
                    <p className='mt-1 text-[11px] text-muted-foreground'>
                      日常调用最常感受到的首字速度，推荐设为{' '}
                      <strong>2200 ~ 2800 ms (默认 2.5秒)</strong>。
                    </p>
                  </div>

                  <div className='rounded-lg border bg-background p-3'>
                    <div className='flex items-center justify-between text-xs font-medium'>
                      <span>③ 日常主要上限（80% 以上请求在此时间内返回）</span>
                      <span className='font-mono text-primary font-semibold'>
                        {(ttft.primary_upper_ms / 1000).toFixed(2)} 秒 (
                        {ttft.primary_upper_ms} ms)
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
                          updateTtftField(
                            'primary_upper_ms',
                            Number(e.target.value)
                          )
                        }
                        className='w-full accent-primary'
                      />
                      <Input
                        type='number'
                        value={ttft.primary_upper_ms}
                        onChange={(e) =>
                          updateTtftField(
                            'primary_upper_ms',
                            Number(e.target.value)
                          )
                        }
                        className='h-7 w-24 text-xs font-mono'
                      />
                    </div>
                    <p className='mt-1 text-[11px] text-muted-foreground'>
                      控制主力分布区的右边界，推荐设为{' '}
                      <strong>4000 ms (4.0秒)</strong>。
                    </p>
                  </div>

                  <div className='rounded-lg border bg-background p-3 space-y-2'>
                    <div className='flex items-center justify-between text-xs font-medium'>
                      <span>④ 常规最慢值 &amp; 偶发长尾散步范围</span>
                      <span className='font-mono text-purple-600 font-semibold'>
                        常规 ≤ {(ttft.normal_max_ms / 1000).toFixed(1)}s · 偶发{' '}
                        {ttft.tail_probability_pct}% 散步至{' '}
                        {(ttft.tail_max_ms / 1000).toFixed(1)}s
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
                            updateTtftField(
                              'normal_max_ms',
                              Number(e.target.value)
                            )
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
                            updateTtftField(
                              'tail_max_ms',
                              Number(e.target.value)
                            )
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
                          updateTtftField(
                            'spread_randomness',
                            Number(e.target.value)
                          )
                        }
                        className='w-full accent-primary'
                      />
                    </div>
                    <p className='mt-1 text-[11px] text-muted-foreground'>
                      自动按随机 Dirichlet 权重混合「偏移对数正态 + 五阶伽马 + 威布尔」三种分布并注入微秒级抖动，数值越高越难被逆向统计。
                    </p>
                  </div>
                </div>

                {/* Right Column: Live Distribution Histogram & Summary Cards */}
                <div className='flex flex-col justify-between space-y-3 lg:col-span-6'>
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
                      <span>
                        {(ttft.primary_upper_ms / 1000).toFixed(1)}s (主上限)
                      </span>
                      <span>
                        {(ttft.normal_max_ms / 1000).toFixed(1)}s (常规上限)
                      </span>
                      <span>
                        {(ttft.tail_max_ms / 1000).toFixed(1)}s (长尾上限)
                      </span>
                    </div>
                  </div>

                  <div className='rounded-lg border bg-background p-3'>
                    <div className='text-xs font-medium mb-1'>
                      最近 12 次连续请求的首字延迟抽样序列（含反聚簇排斥与微秒随机抖动）：
                    </div>
                    <div className='flex flex-wrap gap-1.5 font-mono text-xs'>
                      {activePreview.sample_series_ms
                        .slice(0, 12)
                        .map((ms, idx) => (
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
          )}

          {/* =====================================================================
              MODULE 2: Full Disguise & Mixed-Pool Rules UI (Directly Openable & Editable)
             ===================================================================== */}
          {(activeTab === 'all' || activeTab === 'rules') && (
            <div className='rounded-xl border bg-muted/15 p-4 space-y-4'>
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <div>
                  <h4 className='text-sm font-semibold'>
                    🎭 2. 模型伪装与混池分流规则配置（完整伪装控制台）
                  </h4>
                  <p className='text-xs text-muted-foreground'>
                    由于目前 <code>astra</code> 与 <code>sol</code> 在 CPA1 主池比 Argon 更便宜，<strong>混池规则默认处于未开启状态</strong>，但全部伪装参数、上游模型映射与思考强度（Effort）预算表均在此完整保留，您可随时点击卡片直接修改或一键开启。
                  </p>
                </div>

                <div className='flex flex-wrap items-center gap-1.5'>
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    className='h-7 text-xs'
                    onClick={() => addNewRule('cpa1_disguise')}
                  >
                    + 新增 CPA1 内部跨模型伪装
                  </Button>
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    className='h-7 text-xs'
                    onClick={() => addNewRule('cpa2_forced')}
                  >
                    + 新增 CPA2 Argon 强制伪装
                  </Button>
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    className='h-7 text-xs'
                    onClick={() => addNewRule('weighted_mix')}
                  >
                    + 新增 CPA1+CPA2 权重混池
                  </Button>
                </div>
              </div>

              <div className='space-y-3'>
                {config.rules.map((rule, rIdx) => {
                  const isExpanded = Boolean(expandedRuleIds[rule.id])
                  const toggleExpand = () =>
                    setExpandedRuleIds((prev) => ({
                      ...prev,
                      [rule.id]: !prev[rule.id],
                    }))

                  return (
                    <div
                      key={rule.id}
                      className={`rounded-xl border transition-all ${
                        rule.enabled
                          ? 'border-emerald-500/40 bg-background shadow-2xs'
                          : 'border-border bg-background/80'
                      }`}
                    >
                      {/* Rule Header Bar */}
                      <div className='flex flex-wrap items-center justify-between gap-3 p-3.5'>
                        <div className='flex flex-wrap items-center gap-2.5'>
                          <label className='inline-flex items-center gap-2 cursor-pointer select-none'>
                            <input
                              type='checkbox'
                              checked={rule.enabled}
                              onChange={(e) =>
                                updateRule(rIdx, (r) => ({
                                  ...r,
                                  enabled: e.target.checked,
                                }))
                              }
                              className='h-4 w-4 accent-emerald-600'
                            />
                            <span className='text-sm font-semibold'>
                              {rule.name}
                            </span>
                          </label>

                          {rule.enabled ? (
                            <Badge className='bg-emerald-600 text-white'>
                              已启用
                            </Badge>
                          ) : (
                            <Badge variant='secondary'>
                              未开启（点击左侧勾选即可启用）
                            </Badge>
                          )}

                          <Badge variant='outline' className='font-mono text-xs'>
                            模式:{' '}
                            {rule.mode === 'weighted_mix'
                              ? '权重混池 (weighted_mix)'
                              : rule.mode === 'forced_disguise'
                                ? '强制伪装 (forced_disguise)'
                                : '原生直通 (direct_only)'}
                          </Badge>

                          <span className='text-xs font-mono text-muted-foreground'>
                            匹配: {rule.model_patterns.join(', ')}
                          </span>
                        </div>

                        <div className='flex items-center gap-2'>
                          <Button
                            type='button'
                            size='sm'
                            variant='outline'
                            className='h-7 text-xs'
                            onClick={toggleExpand}
                          >
                            {isExpanded
                              ? '收起伪装详情 ▲'
                              : '⚙️ 展开伪装与思考强度配置 ▼'}
                          </Button>
                          {rule.id !== 'rule-other-models-direct' && (
                            <Button
                              type='button'
                              size='sm'
                              variant='ghost'
                              className='h-7 text-xs text-destructive'
                              onClick={() => removeRule(rIdx)}
                            >
                              删除
                            </Button>
                          )}
                        </div>
                      </div>

                      {/* Expanded Full Disguise Editor */}
                      {isExpanded && (
                        <div className='border-t bg-muted/10 p-4 space-y-4'>
                          {/* Row 1: Basic Rule Match & Mode */}
                          <div className='grid grid-cols-1 gap-3 sm:grid-cols-4'>
                            <div>
                              <label className='text-[11px] font-medium text-muted-foreground'>
                                规则名称
                              </label>
                              <Input
                                value={rule.name}
                                onChange={(e) =>
                                  updateRule(rIdx, (r) => ({
                                    ...r,
                                    name: e.target.value,
                                  }))
                                }
                                className='h-8 text-xs'
                              />
                            </div>
                            <div>
                              <label className='text-[11px] font-medium text-muted-foreground'>
                                匹配模型通配符（逗号分隔，如 *-sol, *-terra）
                              </label>
                              <Input
                                value={rule.model_patterns.join(', ')}
                                onChange={(e) =>
                                  updateRule(rIdx, (r) => ({
                                    ...r,
                                    model_patterns: e.target.value
                                      .split(',')
                                      .map((s) => s.trim())
                                      .filter(Boolean),
                                  }))
                                }
                                className='h-8 text-xs font-mono'
                              />
                            </div>
                            <div>
                              <label className='text-[11px] font-medium text-muted-foreground'>
                                路由与伪装模式
                              </label>
                              <select
                                className='flex h-8 w-full rounded-md border border-input bg-background px-2 text-xs'
                                value={rule.mode}
                                onChange={(e) =>
                                  updateRule(rIdx, (r) => ({
                                    ...r,
                                    mode: e.target
                                      .value as ModelRoutingRule['mode'],
                                  }))
                                }
                              >
                                <option value='weighted_mix'>
                                  权重混池分流 (weighted_mix)
                                </option>
                                <option value='forced_disguise'>
                                  100% 强制伪装映射 (forced_disguise)
                                </option>
                                <option value='direct_only'>
                                  100% 原生直通不改写 (direct_only)
                                </option>
                              </select>
                            </div>
                            <div>
                              <label className='text-[11px] font-medium text-muted-foreground'>
                                优先级（数字越小越先匹配）
                              </label>
                              <Input
                                type='number'
                                value={rule.priority}
                                onChange={(e) =>
                                  updateRule(rIdx, (r) => ({
                                    ...r,
                                    priority: Number(e.target.value),
                                  }))
                                }
                                className='h-8 text-xs font-mono'
                              />
                            </div>
                          </div>

                          {/* Row 2: Sticky Session Policy */}
                          <div className='rounded-lg border bg-background p-3'>
                            <div className='flex flex-wrap items-center justify-between gap-3'>
                              <label className='inline-flex items-center gap-2 text-xs font-medium cursor-pointer'>
                                <input
                                  type='checkbox'
                                  checked={rule.sticky_session.enabled}
                                  onChange={(e) =>
                                    updateRule(rIdx, (r) => ({
                                      ...r,
                                      sticky_session: {
                                        ...r.sticky_session,
                                        enabled: e.target.checked,
                                      },
                                    }))
                                  }
                                />
                                启用会话软粘滞（同一对话保持在同一号池，避免多轮上下文割裂）
                              </label>

                              <div className='flex flex-wrap items-center gap-3 text-xs'>
                                <span>
                                  最少保持轮数:
                                  <Input
                                    type='number'
                                    value={rule.sticky_session.min_soft_turns}
                                    onChange={(e) =>
                                      updateRule(rIdx, (r) => ({
                                        ...r,
                                        sticky_session: {
                                          ...r.sticky_session,
                                          min_soft_turns: Number(
                                            e.target.value
                                          ),
                                        },
                                      }))
                                    }
                                    className='ml-1 inline-block h-7 w-16 text-xs font-mono'
                                  />
                                </span>
                                <span>
                                  最多保持轮数:
                                  <Input
                                    type='number'
                                    value={rule.sticky_session.max_soft_turns}
                                    onChange={(e) =>
                                      updateRule(rIdx, (r) => ({
                                        ...r,
                                        sticky_session: {
                                          ...r.sticky_session,
                                          max_soft_turns: Number(
                                            e.target.value
                                          ),
                                        },
                                      }))
                                    }
                                    className='ml-1 inline-block h-7 w-16 text-xs font-mono'
                                  />
                                </span>
                                <label className='inline-flex items-center gap-1.5 cursor-pointer'>
                                  <input
                                    type='checkbox'
                                    checked={
                                      rule.sticky_session.failover_on_error
                                    }
                                    onChange={(e) =>
                                      updateRule(rIdx, (r) => ({
                                        ...r,
                                        sticky_session: {
                                          ...r.sticky_session,
                                          failover_on_error: e.target.checked,
                                        },
                                      }))
                                    }
                                  />
                                  遇 503/429 立即跨池容灾
                                </label>
                              </div>
                            </div>
                          </div>

                          {/* Row 3: Upstream Pool Targets & Disguise Settings */}
                          <div className='space-y-3'>
                            <div className='text-xs font-semibold'>
                              🎯 目标上游池与伪装参数明细：
                            </div>
                            {rule.targets.map((target, tIdx) => (
                              <div
                                key={target.target_id || tIdx}
                                className='rounded-lg border bg-background p-3.5 space-y-3'
                              >
                                <div className='grid grid-cols-1 gap-3 sm:grid-cols-5'>
                                  <div>
                                    <label className='text-[11px] text-muted-foreground'>
                                      目标上游号池
                                    </label>
                                    <select
                                      className='flex h-8 w-full rounded-md border border-input bg-background px-2 text-xs font-mono'
                                      value={target.pool_id}
                                      onChange={(e) =>
                                        updateRule(rIdx, (r) => ({
                                          ...r,
                                          targets: r.targets.map((t, i) =>
                                            i === tIdx
                                              ? {
                                                  ...t,
                                                  pool_id: e.target.value,
                                                }
                                              : t
                                          ),
                                        }))
                                      }
                                    >
                                      {config.pools.map((p) => (
                                        <option key={p.id} value={p.id}>
                                          {p.id} ({p.name})
                                        </option>
                                      ))}
                                    </select>
                                  </div>

                                  <div>
                                    <label className='text-[11px] text-muted-foreground'>
                                      分流权重 (0 ~ 100)
                                    </label>
                                    <div className='flex items-center gap-2'>
                                      <input
                                        type='range'
                                        min={0}
                                        max={100}
                                        step={5}
                                        value={target.weight}
                                        onChange={(e) =>
                                          updateRule(rIdx, (r) => ({
                                            ...r,
                                            targets: r.targets.map((t, i) =>
                                              i === tIdx
                                                ? {
                                                    ...t,
                                                    weight: Number(
                                                      e.target.value
                                                    ),
                                                  }
                                                : t
                                            ),
                                          }))
                                        }
                                        className='w-full accent-primary'
                                      />
                                      <Input
                                        type='number'
                                        value={target.weight}
                                        onChange={(e) =>
                                          updateRule(rIdx, (r) => ({
                                            ...r,
                                            targets: r.targets.map((t, i) =>
                                              i === tIdx
                                                ? {
                                                    ...t,
                                                    weight: Number(
                                                      e.target.value
                                                    ),
                                                  }
                                                : t
                                            ),
                                          }))
                                        }
                                        className='h-8 w-16 text-xs font-mono'
                                      />
                                    </div>
                                  </div>

                                  <div>
                                    <label className='text-[11px] text-muted-foreground'>
                                      实际请求上游模型 (upstream_model)
                                    </label>
                                    <Input
                                      value={target.upstream_model}
                                      onChange={(e) =>
                                        updateRule(rIdx, (r) => ({
                                          ...r,
                                          targets: r.targets.map((t, i) =>
                                            i === tIdx
                                              ? {
                                                  ...t,
                                                  upstream_model:
                                                    e.target.value,
                                                }
                                              : t
                                          ),
                                        }))
                                      }
                                      placeholder='如 gemini-4-argon-high 或 $clean_model'
                                      className='h-8 text-xs font-mono'
                                    />
                                  </div>

                                  <div>
                                    <label className='text-[11px] text-muted-foreground'>
                                      伪装协议转换族
                                    </label>
                                    <select
                                      className='flex h-8 w-full rounded-md border border-input bg-background px-2 text-xs font-mono'
                                      value={target.protocol_family}
                                      onChange={(e) =>
                                        updateRule(rIdx, (r) => ({
                                          ...r,
                                          targets: r.targets.map((t, i) =>
                                            i === tIdx
                                              ? {
                                                  ...t,
                                                  protocol_family:
                                                    e.target.value,
                                                }
                                              : t
                                          ),
                                        }))
                                      }
                                    >
                                      <option value='openai_native'>
                                        openai_native (CPA1 原生/同构伪装)
                                      </option>
                                      <option value='argon_cpa'>
                                        argon_cpa (CPA2 Gemini/Argon 深度伪装)
                                      </option>
                                    </select>
                                  </div>

                                  <div>
                                    <label className='text-[11px] text-muted-foreground'>
                                      客户端展示模型名 (masquerade_model)
                                    </label>
                                    <Input
                                      value={target.masquerade_model}
                                      onChange={(e) =>
                                        updateRule(rIdx, (r) => ({
                                          ...r,
                                          targets: r.targets.map((t, i) =>
                                            i === tIdx
                                              ? {
                                                  ...t,
                                                  masquerade_model:
                                                    e.target.value,
                                                }
                                              : t
                                          ),
                                        }))
                                      }
                                      placeholder='$clean_model'
                                      className='h-8 text-xs font-mono'
                                    />
                                  </div>
                                </div>

                                {/* Disguise Checkboxes */}
                                <div className='flex flex-wrap items-center gap-5 pt-1 text-xs'>
                                  <label className='inline-flex items-center gap-1.5 cursor-pointer font-medium'>
                                    <input
                                      type='checkbox'
                                      checked={target.disguise_enabled}
                                      onChange={(e) =>
                                        updateRule(rIdx, (r) => ({
                                          ...r,
                                          targets: r.targets.map((t, i) =>
                                            i === tIdx
                                              ? {
                                                  ...t,
                                                  disguise_enabled:
                                                    e.target.checked,
                                                }
                                              : t
                                          ),
                                        }))
                                      }
                                    />
                                    开启伪装改写 (disguise_enabled)
                                  </label>

                                  <label className='inline-flex items-center gap-1.5 cursor-pointer'>
                                    <input
                                      type='checkbox'
                                      checked={target.inject_identity_prompt}
                                      onChange={(e) =>
                                        updateRule(rIdx, (r) => ({
                                          ...r,
                                          targets: r.targets.map((t, i) =>
                                            i === tIdx
                                              ? {
                                                  ...t,
                                                  inject_identity_prompt:
                                                    e.target.checked,
                                                }
                                              : t
                                          ),
                                        }))
                                      }
                                    />
                                    自动注入 OpenAI 身份防套话指令
                                  </label>

                                  <label className='inline-flex items-center gap-1.5 cursor-pointer'>
                                    <input
                                      type='checkbox'
                                      checked={target.scrub_response_identity}
                                      onChange={(e) =>
                                        updateRule(rIdx, (r) => ({
                                          ...r,
                                          targets: r.targets.map((t, i) =>
                                            i === tIdx
                                              ? {
                                                  ...t,
                                                  scrub_response_identity:
                                                    e.target.checked,
                                                }
                                              : t
                                          ),
                                        }))
                                      }
                                    />
                                    自动抹除响应中的 Gemini / Argon / DeepMind 痕迹
                                  </label>
                                </div>

                                {/* Effort Mapping Table */}
                                {target.effort_map &&
                                  target.effort_map.length > 0 && (
                                    <div className='rounded-md border bg-muted/20 p-2.5 space-y-2'>
                                      <div className='text-[11px] font-semibold text-muted-foreground'>
                                        🧠 多对多思考强度 (Reasoning Effort) 与 Thinking Budget 映射表：
                                      </div>
                                      <div className='overflow-x-auto'>
                                        <table className='w-full text-xs'>
                                          <thead>
                                            <tr className='border-b text-left text-muted-foreground'>
                                              <th className='p-1'>
                                                下游请求强度 (逗号分隔)
                                              </th>
                                              <th className='p-1'>
                                                覆盖上游模型名 (含预算后缀)
                                              </th>
                                              <th className='p-1'>
                                                上游 effort
                                              </th>
                                              <th className='p-1'>
                                                Thinking Budget
                                              </th>
                                            </tr>
                                          </thead>
                                          <tbody>
                                            {target.effort_map.map(
                                              (em, eIdx) => (
                                                <tr
                                                  key={eIdx}
                                                  className='border-b last:border-0'
                                                >
                                                  <td className='p-1'>
                                                    <Input
                                                      value={em.downstream_efforts.join(
                                                        ', '
                                                      )}
                                                      onChange={(e) =>
                                                        updateRule(
                                                          rIdx,
                                                          (r) => ({
                                                            ...r,
                                                            targets:
                                                              r.targets.map(
                                                                (t, i) =>
                                                                  i === tIdx
                                                                    ? {
                                                                        ...t,
                                                                        effort_map:
                                                                          t.effort_map.map(
                                                                            (
                                                                              m,
                                                                              mi
                                                                            ) =>
                                                                              mi ===
                                                                              eIdx
                                                                                ? {
                                                                                    ...m,
                                                                                    downstream_efforts:
                                                                                      e.target.value
                                                                                        .split(
                                                                                          ','
                                                                                        )
                                                                                        .map(
                                                                                          (
                                                                                            s
                                                                                          ) =>
                                                                                            s.trim()
                                                                                        )
                                                                                        .filter(
                                                                                          Boolean
                                                                                        ),
                                                                                  }
                                                                                : m
                                                                          ),
                                                                      }
                                                                    : t
                                                              ),
                                                          })
                                                        )
                                                      }
                                                      className='h-7 text-xs font-mono'
                                                    />
                                                  </td>
                                                  <td className='p-1'>
                                                    <Input
                                                      value={
                                                        em.upstream_model_override
                                                      }
                                                      onChange={(e) =>
                                                        updateRule(
                                                          rIdx,
                                                          (r) => ({
                                                            ...r,
                                                            targets:
                                                              r.targets.map(
                                                                (t, i) =>
                                                                  i === tIdx
                                                                    ? {
                                                                        ...t,
                                                                        effort_map:
                                                                          t.effort_map.map(
                                                                            (
                                                                              m,
                                                                              mi
                                                                            ) =>
                                                                              mi ===
                                                                              eIdx
                                                                                ? {
                                                                                    ...m,
                                                                                    upstream_model_override:
                                                                                      e
                                                                                        .target
                                                                                        .value,
                                                                                  }
                                                                                : m
                                                                          ),
                                                                      }
                                                                    : t
                                                              ),
                                                          })
                                                        )
                                                      }
                                                      className='h-7 text-xs font-mono'
                                                    />
                                                  </td>
                                                  <td className='p-1'>
                                                    <Input
                                                      value={em.upstream_effort}
                                                      onChange={(e) =>
                                                        updateRule(
                                                          rIdx,
                                                          (r) => ({
                                                            ...r,
                                                            targets:
                                                              r.targets.map(
                                                                (t, i) =>
                                                                  i === tIdx
                                                                    ? {
                                                                        ...t,
                                                                        effort_map:
                                                                          t.effort_map.map(
                                                                            (
                                                                              m,
                                                                              mi
                                                                            ) =>
                                                                              mi ===
                                                                              eIdx
                                                                                ? {
                                                                                    ...m,
                                                                                    upstream_effort:
                                                                                      e
                                                                                        .target
                                                                                        .value,
                                                                                  }
                                                                                : m
                                                                          ),
                                                                      }
                                                                    : t
                                                              ),
                                                          })
                                                        )
                                                      }
                                                      className='h-7 w-24 text-xs font-mono'
                                                    />
                                                  </td>
                                                  <td className='p-1'>
                                                    <Input
                                                      type='number'
                                                      value={
                                                        em.thinking_budget ?? ''
                                                      }
                                                      onChange={(e) =>
                                                        updateRule(
                                                          rIdx,
                                                          (r) => ({
                                                            ...r,
                                                            targets:
                                                              r.targets.map(
                                                                (t, i) =>
                                                                  i === tIdx
                                                                    ? {
                                                                        ...t,
                                                                        effort_map:
                                                                          t.effort_map.map(
                                                                            (
                                                                              m,
                                                                              mi
                                                                            ) =>
                                                                              mi ===
                                                                              eIdx
                                                                                ? {
                                                                                    ...m,
                                                                                    thinking_budget:
                                                                                      e.target.value.trim() ===
                                                                                      ''
                                                                                        ? null
                                                                                        : Number(
                                                                                            e
                                                                                              .target
                                                                                              .value
                                                                                          ),
                                                                                  }
                                                                                : m
                                                                          ),
                                                                      }
                                                                    : t
                                                              ),
                                                          })
                                                        )
                                                      }
                                                      className='h-7 w-28 text-xs font-mono'
                                                    />
                                                  </td>
                                                </tr>
                                              )
                                            )}
                                          </tbody>
                                        </table>
                                      </div>
                                    </div>
                                  )}
                              </div>
                            ))}
                          </div>
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>
            </div>
          )}

          {/* =====================================================================
              MODULE 3: Upstream Pools Management (CPA1 / CPA2)
             ===================================================================== */}
          {(activeTab === 'all' || activeTab === 'pools') && (
            <div className='rounded-xl border bg-muted/15 p-4 space-y-3'>
              <div>
                <h4 className='text-sm font-semibold'>
                  🌐 3. 上游号池配置 (CPA1 主池 / CPA2 Argon 备用伪装池)
                </h4>
                <p className='text-xs text-muted-foreground'>
                  配置各号池网关地址与访问密钥。即使 CPA2 号池处于就绪状态，只要上方混池或强制伪装规则未勾选启用，常规请求均不会发往 CPA2。
                </p>
              </div>

              <div className='grid grid-cols-1 gap-3 md:grid-cols-2'>
                {config.pools.map((pool, pIdx) => (
                  <div
                    key={pool.id}
                    className='rounded-lg border bg-background p-3.5 space-y-2.5'
                  >
                    <div className='flex items-center justify-between'>
                      <div className='flex items-center gap-2'>
                        <Badge variant='outline' className='font-mono'>
                          {pool.id}
                        </Badge>
                        <span className='text-sm font-semibold'>
                          {pool.name}
                        </span>
                      </div>
                      <label className='inline-flex items-center gap-1.5 text-xs cursor-pointer'>
                        <input
                          type='checkbox'
                          checked={pool.enabled}
                          onChange={(e) =>
                            updatePool(pIdx, 'enabled', e.target.checked)
                          }
                        />
                        池已就绪
                      </label>
                    </div>

                    <div className='grid grid-cols-1 gap-2 sm:grid-cols-2'>
                      <div>
                        <label className='text-[11px] text-muted-foreground'>
                          Base URL
                        </label>
                        <Input
                          value={pool.base_url}
                          onChange={(e) =>
                            updatePool(pIdx, 'base_url', e.target.value)
                          }
                          className='h-8 text-xs font-mono'
                        />
                      </div>
                      <div>
                        <label className='text-[11px] text-muted-foreground'>
                          API Key（留空则透传渠道 Key）
                        </label>
                        <Input
                          type='password'
                          value={pool.api_key}
                          onChange={(e) =>
                            updatePool(pIdx, 'api_key', e.target.value)
                          }
                          placeholder='未设置或沿用环境变量'
                          className='h-8 text-xs font-mono'
                        />
                      </div>
                    </div>
                    <p className='text-[11px] text-muted-foreground'>
                      {pool.description}
                    </p>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* =====================================================================
              MODULE 4: Route Simulator & Captured Request Samples
             ===================================================================== */}
          {(activeTab === 'all' || activeTab === 'diag') && (
            <div className='rounded-xl border bg-muted/15 p-4 space-y-4'>
              <div>
                <h4 className='text-sm font-semibold'>
                  🔍 4. 路由命中仿真测试器与实时请求抓包对比
                </h4>
                <p className='text-xs text-muted-foreground'>
                  无需消耗外部上游配额即可验证任意模型与思考强度的路由去向，或查看最近真实请求的伪装改写前后对比。
                </p>
              </div>

              <div className='flex flex-wrap items-end gap-2.5 rounded-lg border bg-background p-3'>
                <div>
                  <label className='text-[11px] text-muted-foreground'>
                    测试下游模型名
                  </label>
                  <Input
                    value={simModel}
                    onChange={(e) => setSimModel(e.target.value)}
                    className='h-8 w-44 text-xs font-mono'
                  />
                </div>
                <div>
                  <label className='text-[11px] text-muted-foreground'>
                    思考强度 (effort)
                  </label>
                  <select
                    className='flex h-8 rounded-md border border-input bg-background px-2 text-xs font-mono'
                    value={simEffort}
                    onChange={(e) => setSimEffort(e.target.value)}
                  >
                    {[
                      'none',
                      'minimal',
                      'low',
                      'medium',
                      'high',
                      'xhigh',
                      'max',
                    ].map((ef) => (
                      <option key={ef} value={ef}>
                        {ef}
                      </option>
                    ))}
                  </select>
                </div>
                <label className='flex items-center gap-1.5 text-xs pb-1.5 cursor-pointer'>
                  <input
                    type='checkbox'
                    checked={simForceArgon}
                    onChange={(e) => setSimForceArgon(e.target.checked)}
                  />
                  携带 X-Force-Argon 测试头
                </label>
                <Button
                  type='button'
                  size='sm'
                  variant='secondary'
                  onClick={() => void runSimulation(1)}
                >
                  单次路由仿真
                </Button>
                <Button
                  type='button'
                  size='sm'
                  variant='outline'
                  onClick={() => void runSimulation(40)}
                >
                  📊 40 次新会话分流比抽样
                </Button>
                {simOutput && (
                  <div className='w-full mt-2 rounded border bg-muted/30 p-2.5 font-mono text-xs'>
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
                          <th className='p-1.5'>接口</th>
                          <th className='p-1.5'>下游模型 &amp; 强度</th>
                          <th className='p-1.5'>实际目标池 &amp; 上游模型</th>
                          <th className='p-1.5'>伪装状态</th>
                          <th className='p-1.5'>操作</th>
                        </tr>
                      </thead>
                      <tbody>
                        {samples.map((s, idx) => (
                          <tr key={s.id} className='border-b'>
                            <td className='p-1.5'>
                              {new Date(
                                s.captured_at_unix * 1000
                              ).toLocaleTimeString()}
                            </td>
                            <td className='p-1.5 font-mono'>
                              {s.endpoint_path}
                            </td>
                            <td className='p-1.5 font-mono'>
                              {s.downstream_model} ({s.downstream_effort})
                            </td>
                            <td className='p-1.5 font-mono'>
                              {s.selected_pool_id} ➔ {s.upstream_model}
                            </td>
                            <td className='p-1.5'>
                              {s.disguise_enabled ? (
                                <Badge className='bg-amber-600 text-white'>
                                  已伪装
                                </Badge>
                              ) : (
                                <Badge variant='outline'>CPA1 原生</Badge>
                              )}
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
                                {selectedSampleIdx === idx
                                  ? '收起对比'
                                  : '查看改写对比'}
                              </Button>
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
                {selectedSampleIdx !== null && samples[selectedSampleIdx] && (
                  <div className='grid grid-cols-1 gap-2 pt-2 md:grid-cols-2'>
                    <div>
                      <div className='text-[11px] font-medium text-muted-foreground mb-1'>
                        原始客户端请求体预览:
                      </div>
                      <pre className='max-h-52 overflow-auto rounded border bg-muted/40 p-2 font-mono text-[11px] whitespace-pre-wrap'>
                        {samples[selectedSampleIdx].raw_body_preview}
                      </pre>
                    </div>
                    <div>
                      <div className='text-[11px] font-medium text-muted-foreground mb-1'>
                        伪装/改写后发往上游请求体预览:
                      </div>
                      <pre className='max-h-52 overflow-auto rounded border bg-muted/40 p-2 font-mono text-[11px] whitespace-pre-wrap'>
                        {samples[selectedSampleIdx].rewritten_body_preview}
                      </pre>
                    </div>
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
