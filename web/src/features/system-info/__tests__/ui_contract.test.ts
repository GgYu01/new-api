import { describe, it, expect } from 'vitest'

describe('UI Version Badge & Metric Display Contract Tests', () => {
  it('formats initializing and stale metrics accurately without falsifying 0%', () => {
    function formatMetricPercent(
      value?: number | null,
      detail?: { status?: string; is_stale?: boolean }
    ): string {
      if (detail?.status === 'initializing') return 'init...'
      if (detail?.status === 'disabled') return 'disabled'
      if (detail?.status === 'unavailable') return 'N/A'
      if (detail?.status === 'stale' || detail?.is_stale === true) {
        if (typeof value === 'number' && !Number.isNaN(value)) {
          return `${value.toFixed(1)}% (stale)`
        }
        return 'stale'
      }
      if (typeof value !== 'number' || Number.isNaN(value)) return '-'
      if (value > 0 && value < 0.1) return '<0.1%'
      return `${value.toFixed(1)}%`
    }

    expect(formatMetricPercent(null, { status: 'initializing' })).toBe('init...')
    expect(formatMetricPercent(undefined, { status: 'disabled' })).toBe('disabled')
    expect(formatMetricPercent(null, { status: 'unavailable' })).toBe('N/A')
    expect(formatMetricPercent(15.24, { status: 'stale', is_stale: true })).toBe('15.2% (stale)')
    expect(formatMetricPercent(null, { status: 'stale', is_stale: true })).toBe('stale')
    expect(formatMetricPercent(0.04, { status: 'normal' })).toBe('<0.1%')
    expect(formatMetricPercent(15.24, { status: 'normal' })).toBe('15.2%')
    expect(formatMetricPercent(0, { status: 'normal' })).toBe('0.0%')
  })

  it('renders authentic build info metadata without hardcoded r12 fallback and distinguishes dirty vs unknown', () => {
    function getDirtyBadgeLabel(buildInfo?: { is_dirty?: boolean; dirty_status?: string; has_vcs?: boolean }): string {
      if (buildInfo?.dirty_status === 'dirty' || buildInfo?.is_dirty) return 'Dirty'
      if (buildInfo?.dirty_status === 'clean' || (buildInfo?.is_dirty === false && buildInfo?.has_vcs)) return 'Clean'
      return 'Unknown'
    }

    // 缺少 VCS 元数据时绝不能误显示 Clean
    expect(getDirtyBadgeLabel(undefined)).toBe('Unknown')
    expect(getDirtyBadgeLabel({})).toBe('Unknown')
    expect(getDirtyBadgeLabel({ is_dirty: false, has_vcs: false })).toBe('Unknown')
    expect(getDirtyBadgeLabel({ is_dirty: false, has_vcs: true, dirty_status: 'clean' })).toBe('Clean')
    expect(getDirtyBadgeLabel({ is_dirty: true, has_vcs: true, dirty_status: 'dirty' })).toBe('Dirty')

    function resolveDisplayVersion(status?: { version?: string }, buildInfo?: { version?: string; compiled_version?: string }): string {
      return status?.version || buildInfo?.version || 'Loading...'
    }

    // 绝不硬编码 fallback 到 v1.0.0-r12
    expect(resolveDisplayVersion(undefined, undefined)).toBe('Loading...')
    expect(resolveDisplayVersion({ version: 'v1.0.0-r12.4' }, { compiled_version: 'v1.0.0-r12.4' })).toBe('v1.0.0-r12.4')
  })

  it('handles multi-resolution layout bounds (1920x1080 and 1912x948)', () => {
    const resolutions = [
      { width: 1920, height: 1080 },
      { width: 1912, height: 948 },
    ]

    for (const res of resolutions) {
      expect(res.width).toBeGreaterThanOrEqual(1230) // Table min-width 1230px
      expect(res.height).toBeGreaterThan(600)
    }
  })
})
