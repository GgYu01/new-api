/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/
import { zodResolver } from '@hookform/resolvers/zod'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage } from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/api'

import { SettingsForm, SettingsSwitchContent, SettingsSwitchItem } from '../components/settings-form-layout'
import { SettingsSection } from '../components/settings-section'
import { safeNumberFieldProps } from '../utils/numeric-field'

const schema = z.object({
  enabled: z.boolean(),
  mode: z.enum(['off', 'rpm', 'concurrency', 'hybrid']),
  global_rpm: z.coerce.number().int().min(0),
  burst: z.coerce.number().int().min(0),
  max_active_requests: z.coerce.number().int().min(0),
})

type FormInput = z.input<typeof schema>
type Values = z.output<typeof schema>
type Props = { defaultValues: FormInput }
type Runtime = {
  active_current: number
  active_peak: number
  admitted_total: number
  rejected_rpm_total: number
  rejected_active_total: number
  revision: number
  config: {
    enabled: boolean
    mode: 'off' | 'rpm' | 'concurrency' | 'hybrid'
    global_rpm: number
    burst: number
    max_active_requests: number
    waiting_queue: number
    waiting_timeout_ms: number
  }
}

export function TrafficControlSection({ defaultValues }: Props) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [runtime, setRuntime] = useState<Runtime | null>(null)
  const revisionRef = useRef<number | null>(null)
  const form = useForm<FormInput, unknown, Values>({ resolver: zodResolver(schema), defaultValues })

  const refresh = useCallback(async () => {
    try {
      const response = await api.get('/api/option/traffic-control')
      if (response.data.success) setRuntime(response.data.data)
    } catch { /* keep the last runtime snapshot */ }
  }, [])
  useEffect(() => { void refresh() }, [refresh])
  useEffect(() => { if (runtime) revisionRef.current = runtime.revision }, [runtime])

  // Adopt server-returned config after a successful save so the form always
  // reflects the effective runtime values, then refresh the shared options
  // cache so re-entering the page reads the same source of truth.
  const adoptServerConfig = useCallback((data: Runtime) => {
    setRuntime(data)
    revisionRef.current = data.revision
    form.reset({
      enabled: data.config.enabled,
      mode: data.config.mode,
      global_rpm: data.config.global_rpm,
      burst: data.config.burst,
      max_active_requests: data.config.max_active_requests,
    })
    void queryClient.invalidateQueries({ queryKey: ['system-options'] })
  }, [form, queryClient])

  // Sync the form when the shared GET cache changes, but never overwrite
  // edits the user has not submitted yet.
  useEffect(() => {
    if (!form.formState.isDirty) form.reset(defaultValues)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [defaultValues])

  const onSubmit = async (values: Values) => {
    const payload = {
      ...values,
      waiting_queue: 0,
      waiting_timeout_ms: 0,
      ...(revisionRef.current !== null ? { revision: revisionRef.current } : {}),
    }
    try {
      const response = await api.put('/api/option/traffic-control', payload)
      if (!response.data.success) throw new Error(response.data.message)
      adoptServerConfig(response.data.data)
      toast.success(t('Traffic control updated'))
    } catch (error) {
      const status = (error as { response?: { status?: number } })?.response?.status
      if (status === 409) {
        toast.error(t('Config was changed elsewhere; the saved values have been reloaded.'))
        try {
          const response = await api.get('/api/option/traffic-control')
          if (response.data.success) adoptServerConfig(response.data.data)
        } catch { /* keep local edits visible */ }
        return
      }
      toast.error(error instanceof Error ? error.message : t('Failed to update setting'))
    }
  }

  const mode = form.watch('mode')
  const rpmDisabled = mode !== 'rpm' && mode !== 'hybrid'
  const activeDisabled = mode !== 'concurrency' && mode !== 'hybrid'
  const maxActive = form.watch('max_active_requests')
  const activeCurrent = runtime?.active_current ?? 0
  return (
    <SettingsSection title={t('Traffic Control')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <div className='flex justify-end'>
            <Button type='submit' disabled={form.formState.isSubmitting}>{t('Save')}</Button>
          </div>
          <FormField control={form.control} name='enabled' render={({ field }) => (
            <SettingsSwitchItem><SettingsSwitchContent><FormLabel>{t('Enable traffic control')}</FormLabel><FormDescription>{t('Admission is checked after authentication and before billing.')}</FormDescription></SettingsSwitchContent><FormControl><Switch checked={field.value} onCheckedChange={field.onChange} /></FormControl></SettingsSwitchItem>
          )} />
          <div className='grid grid-cols-1 gap-4 md:grid-cols-2'>
            <FormField control={form.control} name='mode' render={({ field }) => (
              <FormItem><FormLabel>{t('Limit mode')}</FormLabel><Select value={field.value} onValueChange={field.onChange}><FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl><SelectContent><SelectItem value='off'>{t('Off')}</SelectItem><SelectItem value='rpm'>{t('RPM')}</SelectItem><SelectItem value='concurrency'>{t('Concurrent Requests')}</SelectItem><SelectItem value='hybrid'>{t('Hybrid')}</SelectItem></SelectContent></Select><FormDescription>{t('Which global admission control applies to execution requests.')}</FormDescription><FormMessage /></FormItem>
            )} />
            <FormField control={form.control} name='max_active_requests' render={({ field }) => (
              <FormItem><FormLabel>{t('Maximum active requests')}</FormLabel><FormControl><Input type='number' min={0} {...safeNumberFieldProps(field)} disabled={activeDisabled} /></FormControl><FormDescription><abbr title={t('Concurrent Requests = requests still actively executing or streaming at the same time.')}>{t('Concurrent Requests')}</abbr></FormDescription><FormMessage /></FormItem>
            )} />
          </div>
          <div className='rounded-lg border p-4 text-sm'>
            {t('Current active')}: <span className='font-medium'>{activeCurrent}</span>
            {activeDisabled ? '' : <> / {maxActive}</>}
          </div>
          <Collapsible>
            <CollapsibleTrigger className='text-sm font-medium underline-offset-4 hover:underline'>
              {t('Advanced: RPM and Burst')}
            </CollapsibleTrigger>
            <CollapsibleContent className='mt-4 space-y-4'>
              <p className='text-muted-foreground text-sm'>{t('RPM and Burst only apply when the limit mode is RPM or Hybrid. RPM = Requests Per Minute.')}</p>
              <div className='grid grid-cols-1 gap-4 md:grid-cols-2'>
                <FormField control={form.control} name='global_rpm' render={({ field }) => (
                  <FormItem><FormLabel>{t('Global RPM')}</FormLabel><FormControl><Input type='number' min={0} {...safeNumberFieldProps(field)} disabled={rpmDisabled} /></FormControl><FormDescription><abbr title={t('RPM = Requests Per Minute.')}>RPM</abbr></FormDescription><FormMessage /></FormItem>
                )} />
                <FormField control={form.control} name='burst' render={({ field }) => (
                  <FormItem><FormLabel>{t('Burst')}</FormLabel><FormControl><Input type='number' min={0} {...safeNumberFieldProps(field)} disabled={rpmDisabled} /></FormControl><FormDescription><abbr title={t('Burst = immediately available token capacity above the steady refill rate.')}>{t('Burst')}</abbr></FormDescription><FormMessage /></FormItem>
                )} />
              </div>
            </CollapsibleContent>
          </Collapsible>
          <FormItem><FormLabel>{t('Waiting queue')}</FormLabel><Input value={0} disabled /><FormDescription><abbr title={t('Waiting Queue = requests retained while capacity is unavailable; disabled here to reduce resource use.')}>{t('Waiting Queue')}</abbr> ({t('disabled')})</FormDescription></FormItem>
        </SettingsForm>
      </Form>
      {runtime && <div className='mt-4 grid grid-cols-2 gap-2 rounded-lg border p-4 text-sm md:grid-cols-5'><span>{t('Current active')}: {runtime.active_current}</span><span>{t('Peak')}: {runtime.active_peak}</span><span>{t('Admitted')}: {runtime.admitted_total}</span><span>{t('RPM rejected')}: {runtime.rejected_rpm_total}</span><span>{t('Active rejected')}: {runtime.rejected_active_total}</span></div>}
    </SettingsSection>
  )
}
