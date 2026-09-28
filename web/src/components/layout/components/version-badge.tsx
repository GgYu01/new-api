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
import {
  Binary,
  Calendar,
  CheckCircle2,
  Cpu,
  GitBranch,
  Layers,
  Terminal,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import { useStatus } from '@/hooks/use-status'
import { formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'

export function VersionBadge() {
  const { t } = useTranslation()
  const { status } = useStatus()
  const [open, setOpen] = useState(false)

  const buildInfo = status?.build_info
  const version = status?.version || buildInfo?.version || t('Loading...')
  const compiledVersion =
    buildInfo?.compiled_version ||
    (buildInfo?.runtime_override ? t('Unknown') : version)
  const revision = buildInfo?.revision ? buildInfo.revision.slice(0, 8) : ''

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        render={
          <Button
            variant='outline'
            size='xs'
            className={cn(
              'h-6 gap-1 px-2 font-mono text-[11px] font-normal transition-colors select-none',
              'hover:bg-accent hover:text-accent-foreground',
              'border-border/60 bg-background/50'
            )}
            aria-label={t('View version details')}
          >
            <span className='font-semibold text-primary'>{version}</span>
            {revision && (
              <span className='text-muted-foreground hidden sm:inline'>
                ({revision})
              </span>
            )}
          </Button>
        }
      />
      <PopoverContent align='end' side='bottom' className='w-84 p-4'>
        <PopoverHeader className='space-y-1 pb-2 border-b border-border/60'>
          <div className='flex items-center justify-between'>
            <PopoverTitle className='text-sm font-semibold flex items-center gap-1.5'>
              <Layers className='size-4 text-primary' />
              {t('Build & Runtime Info')}
            </PopoverTitle>
            <Badge variant='secondary' className='font-mono text-[10px]'>
              {buildInfo?.dirty_status === 'dirty' || buildInfo?.is_dirty
                ? t('Dirty')
                : buildInfo?.dirty_status === 'clean' ||
                  (buildInfo?.is_dirty === false && buildInfo?.has_vcs)
                ? t('Clean')
                : t('Unknown')}
            </Badge>
          </div>
          <PopoverDescription className='text-xs text-muted-foreground'>
            {t('Authentic binary and runtime metadata')}
          </PopoverDescription>
        </PopoverHeader>

        <div className='mt-3 space-y-2 text-xs'>
          <div className='grid grid-cols-[auto_1fr] gap-x-2 gap-y-1.5 items-center'>
            <span
              className='text-muted-foreground flex items-center gap-1.5'
              title={t('Active runtime version')}
            >
              <Binary className='size-3.5' />
              {t('Version')}:
            </span>
            <span className='font-mono font-medium text-right text-foreground break-all'>
              {version}
            </span>

            <span
              className='text-muted-foreground flex items-center gap-1.5'
              title={t('Compile-time build version')}
            >
              <Layers className='size-3.5' />
              {t('Compiled Version')}:
            </span>
            <span className='font-mono text-right text-foreground break-all'>
              {compiledVersion}
            </span>

            {buildInfo?.runtime_override && (
              <>
                <span
                  className='text-muted-foreground flex items-center gap-1.5 text-amber-500'
                  title={t('Version overridden by environment variable')}
                >
                  <AlertTriangleIcon className='size-3.5' />
                  {t('Runtime Override')}:
                </span>
                <span className='font-mono text-right text-amber-600 dark:text-amber-400'>
                  {buildInfo.runtime_override}
                </span>
              </>
            )}

            <span
              className='text-muted-foreground flex items-center gap-1.5'
              title={t('Git commit hash (VCS)')}
            >
              <GitBranch className='size-3.5' />
              {t('Revision')}:
            </span>
            <span className='font-mono text-right text-foreground'>
              {buildInfo?.revision || t('Unknown')}
            </span>

            {buildInfo?.snapshot_id && (
              <>
                <span
                  className='text-muted-foreground flex items-center gap-1.5'
                  title={t('Immutable snapshot identifier')}
                >
                  <Layers className='size-3.5' />
                  {t('Snapshot ID')}:
                </span>
                <span className='font-mono text-right text-foreground'>
                  {buildInfo.snapshot_id}
                </span>
              </>
            )}

            <span
              className='text-muted-foreground flex items-center gap-1.5'
              title={t('UTC compilation timestamp')}
            >
              <Calendar className='size-3.5' />
              {t('Build Time')}:
            </span>
            <span className='font-mono text-right text-foreground'>
              {buildInfo?.build_time || t('Unknown')}
            </span>

            <span
              className='text-muted-foreground flex items-center gap-1.5'
              title={t('Go compiler toolchain')}
            >
              <Terminal className='size-3.5' />
              {t('Toolchain')}:
            </span>
            <span className='font-mono text-right text-foreground'>
              {buildInfo?.go_version || t('Unknown')}
            </span>

            <span
              className='text-muted-foreground flex items-center gap-1.5'
              title={t('OS and architecture')}
            >
              <Cpu className='size-3.5' />
              {t('Platform')}:
            </span>
            <span className='font-mono text-right text-foreground'>
              {buildInfo?.platform || t('Unknown')}
            </span>

            {status?.start_time && (
              <>
                <span className='text-muted-foreground flex items-center gap-1.5'>
                  <CheckCircle2 className='size-3.5' />
                  {t('Started')}:
                </span>
                <span className='font-mono text-right text-foreground'>
                  {formatTimestampToDate(Number(status.start_time))}
                </span>
              </>
            )}
          </div>
        </div>
      </PopoverContent>
    </Popover>
  )
}

function AlertTriangleIcon(props: { className?: string }) {
  return (
    <svg
      xmlns='http://www.w3.org/2000/svg'
      width='24'
      height='24'
      viewBox='0 0 24 24'
      fill='none'
      stroke='currentColor'
      strokeWidth='2'
      strokeLinecap='round'
      strokeLinejoin='round'
      className={props.className}
    >
      <path d='m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z' />
      <path d='M12 9v4' />
      <path d='M12 17h.01' />
    </svg>
  )
}
