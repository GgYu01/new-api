/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
export type SystemInstanceStatus = 'online' | 'stale'

export type ResourceMetricDetail = {
  status?: string
  scope?: string
  source?: string
  usage_percent?: number | null
  used_value?: number
  total_capacity?: number
  parent_capacity?: number
  used_cores?: number
  total_cores?: number
  process_rss?: number
  go_heap_alloc?: number
  mount_point?: string
  unit?: string
  sampled_at?: number
  interval_ms?: number
  last_error?: string
  is_stale?: boolean
  [key: string]: unknown
}

export type SystemInstanceInfo = {
  schema_version?: number
  node?: {
    name?: string
    source?: string
    manually_configured?: boolean
    should_configure_manually?: boolean
    [key: string]: unknown
  }
  role?: {
    is_master?: boolean
    [key: string]: unknown
  }
  runtime?: {
    version?: string
    goos?: string
    goarch?: string
    started_at?: number
    [key: string]: unknown
  }
  host?: {
    hostname?: string
    [key: string]: unknown
  }
  resources?: {
    cpu?: ResourceMetricDetail
    memory?: ResourceMetricDetail
    storage?: ResourceMetricDetail & {
      total_bytes?: number
      used_bytes?: number
      free_bytes?: number
      used_percent?: number | null
    }
    [key: string]: unknown
  }
  extra?: Record<string, unknown>
  [key: string]: unknown
}

export type SystemInstance = {
  node_name: string
  status: SystemInstanceStatus
  stale_after_seconds: number
  started_at: number
  last_seen_at: number
  info?: SystemInstanceInfo
}

export type SystemInstanceListResponse = {
  success: boolean
  message: string
  data?: SystemInstance[]
}

export type SystemInstanceDeleteResponse = {
  success: boolean
  message: string
  data?: {
    deleted_count: number
  }
}
