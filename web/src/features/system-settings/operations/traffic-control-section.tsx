/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/
import { zodResolver } from '@hookform/resolvers/zod'
import { useCallback, useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import * as z from 'zod'

import { Button } from '@/components/ui/button'
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
}

export function TrafficControlSection({ defaultValues }: Props) {
  const { t } = useTranslation()
  const [runtime, setRuntime] = useState<Runtime | null>(null)
  const form = useForm<FormInput, unknown, Values>({ resolver: zodResolver(schema), defaultValues })

  const refresh = useCallback(async () => {
    try {
      const response = await api.get('/api/option/traffic-control')
      if (response.data.success) setRuntime(response.data.data)
    } catch { /* keep the last runtime snapshot */ }
  }, [])
  useEffect(() => { form.reset(defaultValues); void refresh() }, [defaultValues, form, refresh])

  const onSubmit = async (values: Values) => {
    const payload = { ...values, waiting_queue: 0, waiting_timeout_ms: 0 }
    try {
      const response = await api.put('/api/option/traffic-control', payload)
      if (!response.data.success) throw new Error(response.data.message)
      setRuntime(response.data.data)
      toast.success(t('Traffic control updated'))
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t('Failed to update setting'))
    }
  }

  const mode = form.watch('mode')
  const rpmDisabled = mode !== 'rpm' && mode !== 'hybrid'
  const activeDisabled = mode !== 'concurrency' && mode !== 'hybrid'
  return (
    <SettingsSection title={t('Traffic Control')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <div className='flex justify-end'><Button type='submit'>{t('Save')}</Button></div>
          <FormField control={form.control} name='enabled' render={({ field }) => (
            <SettingsSwitchItem><SettingsSwitchContent><FormLabel>{t('Enable traffic control')}</FormLabel><FormDescription>{t('Admission is checked after authentication and before billing.')}</FormDescription></SettingsSwitchContent><FormControl><Switch checked={field.value} onCheckedChange={field.onChange} /></FormControl></SettingsSwitchItem>
          )} />
          <div className='grid grid-cols-1 gap-4 md:grid-cols-3'>
            <FormField control={form.control} name='mode' render={({ field }) => (
              <FormItem><FormLabel>{t('Mode')}</FormLabel><Select value={field.value} onValueChange={field.onChange}><FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl><SelectContent><SelectItem value='off'>{t('Off')}</SelectItem><SelectItem value='rpm'>{t('RPM')}</SelectItem><SelectItem value='concurrency'>{t('Concurrent Requests')}</SelectItem><SelectItem value='hybrid'>{t('Hybrid')}</SelectItem></SelectContent></Select><FormDescription><abbr title={t('RPM = Requests Per Minute.')}>RPM</abbr> / <abbr title={t('Concurrent Requests = requests still actively executing or streaming at the same time.')}>{t('Concurrent Requests')}</abbr></FormDescription><FormMessage /></FormItem>
            )} />
            <FormField control={form.control} name='global_rpm' render={({ field }) => (<FormItem><FormLabel>{t('Global RPM')}</FormLabel><FormControl><Input type='number' min={0} {...safeNumberFieldProps(field)} disabled={rpmDisabled} /></FormControl><FormDescription><abbr title={t('Burst = immediately available token capacity above the steady refill rate.')}>{t('Burst')}</abbr> {String(form.watch('burst') ?? '')}</FormDescription><FormMessage /></FormItem>)} />
            <FormField control={form.control} name='burst' render={({ field }) => (<FormItem><FormLabel>{t('Burst')}</FormLabel><FormControl><Input type='number' min={0} {...safeNumberFieldProps(field)} disabled={rpmDisabled} /></FormControl><FormMessage /></FormItem>)} />
            <FormField control={form.control} name='max_active_requests' render={({ field }) => (<FormItem><FormLabel>{t('Maximum active requests')}</FormLabel><FormControl><Input type='number' min={0} {...safeNumberFieldProps(field)} disabled={activeDisabled} /></FormControl><FormMessage /></FormItem>)} />
          </div>
          <FormItem><FormLabel>{t('Waiting queue')}</FormLabel><Input value={0} disabled /><FormDescription><abbr title={t('Waiting Queue = requests retained while capacity is unavailable; disabled here to reduce resource use.')}>{t('Waiting Queue')}</abbr> ({t('disabled')})</FormDescription></FormItem>
        </SettingsForm>
      </Form>
      {runtime && <div className='mt-4 grid grid-cols-2 gap-2 rounded-lg border p-4 text-sm md:grid-cols-5'><span>{t('Active')}: {runtime.active_current}</span><span>{t('Peak')}: {runtime.active_peak}</span><span>{t('Admitted')}: {runtime.admitted_total}</span><span>{t('RPM rejected')}: {runtime.rejected_rpm_total}</span><span>{t('Active rejected')}: {runtime.rejected_active_total}</span></div>}
    </SettingsSection>
  )
}
