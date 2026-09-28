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
  CheckCircle2,
  CreditCard,
  Gift,
  Loader2,
  ShieldCheck,
  Sparkles,
  User,
  Wallet,
  Zap,
} from 'lucide-react'
import { useState, useMemo } from 'react'
import { toast } from 'sonner'

import { PublicLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardHeader } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Separator } from '@/components/ui/separator'
import { TitledCard } from '@/components/ui/titled-card'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

interface RechargeTier {
  id: string
  amount: number
  quotaUSD: number
  bonusCNY: number
  tag?: string
  popular?: boolean
  description: string
}

const RECHARGE_TIERS: RechargeTier[] = [
  {
    id: 'tier-200',
    amount: 200,
    quotaUSD: 200,
    bonusCNY: 20,
    tag: '新手优选',
    description: '到账 $200 算力 + 赠 ¥20 体验额度',
  },
  {
    id: 'tier-500',
    amount: 500,
    quotaUSD: 500,
    bonusCNY: 60,
    tag: '特惠推荐',
    description: '到账 $500 算力 + 赠 ¥60 体验额度',
  },
  {
    id: 'tier-1000',
    amount: 1000,
    quotaUSD: 1000,
    bonusCNY: 150,
    tag: '人气热销 🔥',
    popular: true,
    description: '到账 $1,000 算力 + 赠 ¥150 体验额度',
  },
  {
    id: 'tier-5000',
    amount: 5000,
    quotaUSD: 5000,
    bonusCNY: 1000,
    tag: '企业尊享 👑',
    description: '到账 $5,000 算力 + 赠 ¥1,000 体验额度',
  },
]

type PaymentMethodType = 'wechat' | 'alipay' | 'unionpay' | 'applepay'

interface PaymentMethodOption {
  type: PaymentMethodType
  name: string
  iconColor: string
  badgeText?: string
}

const PAYMENT_METHODS: PaymentMethodOption[] = [
  {
    type: 'wechat',
    name: '微信支付',
    iconColor: 'text-emerald-500',
    badgeText: '推荐',
  },
  {
    type: 'alipay',
    name: '支付宝',
    iconColor: 'text-sky-500',
    badgeText: '快捷',
  },
  {
    type: 'unionpay',
    name: '银联云闪付',
    iconColor: 'text-rose-500',
  },
  {
    type: 'applepay',
    name: 'Apple Pay',
    iconColor: 'text-zinc-600 dark:text-zinc-300',
  },
]

interface TransactionReceipt {
  orderId: string
  tier: RechargeTier
  paymentMethod: PaymentMethodOption
  timestamp: string
  finalAmount: number
  totalCredit: number
}

export function RechargePage() {
  const { auth } = useAuthStore()
  const currentUser = auth.user

  const [selectedTier, setSelectedTier] = useState<RechargeTier>(RECHARGE_TIERS[2]) // Default 1000
  const [selectedMethod, setSelectedMethod] = useState<PaymentMethodType>('wechat')
  const [isProcessing, setIsProcessing] = useState(false)
  const [successDialogOpen, setSuccessDialogOpen] = useState(false)
  const [receipt, setReceipt] = useState<TransactionReceipt | null>(null)

  // Local simulated balance for instant gratification (zero effect on server)
  const [simulatedBonusTotal, setSimulatedBonusTotal] = useState(0)

  const currentDisplayBalance = useMemo(() => {
    if (currentUser?.quota) {
      // Quota is in 1/500,000 standard unit or integer
      const baseUSD = currentUser.quota / 500000
      return (baseUSD + simulatedBonusTotal).toFixed(2)
    }
    return (128.5 + simulatedBonusTotal).toFixed(2)
  }, [currentUser, simulatedBonusTotal])

  const handleRecharge = () => {
    if (isProcessing) return

    setIsProcessing(true)

    // Simulate payment gateway handshake (approx 600ms)
    setTimeout(() => {
      const chosenMethod =
        PAYMENT_METHODS.find((m) => m.type === selectedMethod) || PAYMENT_METHODS[0]

      const randomOrderId = `NEWAPI-${new Date().toISOString().slice(0, 10).replaceAll('-', '')}-${Math.floor(100000 + Math.random() * 900000)}`
      const nowStr = new Date().toLocaleString('zh-CN', {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      })

      const newReceipt: TransactionReceipt = {
        orderId: randomOrderId,
        tier: selectedTier,
        paymentMethod: chosenMethod,
        timestamp: nowStr,
        finalAmount: selectedTier.amount,
        totalCredit: selectedTier.quotaUSD + selectedTier.bonusCNY,
      }

      setReceipt(newReceipt)
      setSimulatedBonusTotal((prev) => prev + selectedTier.quotaUSD)
      setIsProcessing(false)
      setSuccessDialogOpen(true)

      toast.success(`🎉 充值成功！¥${selectedTier.amount} 额度已实时入账。`, {
        description: `订单号: ${randomOrderId}`,
        duration: 4000,
      })
    }, 650)
  }

  return (
    <PublicLayout>
      <div className='mx-auto max-w-4xl space-y-6 pb-16'>
        {/* Banner Notice */}
        <div className='relative overflow-hidden rounded-xl border border-primary/20 bg-primary/5 p-4 text-card-foreground shadow-sm backdrop-blur-sm sm:p-5'>
          <div className='flex items-start gap-3 sm:items-center'>
            <div className='flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary'>
              <Sparkles className='h-5 w-5' />
            </div>
            <div className='min-w-0 flex-1'>
              <div className='flex flex-wrap items-center gap-2'>
                <h3 className='text-sm font-semibold tracking-tight sm:text-base'>
                  New API 局域网快速充值中心
                </h3>
                <Badge variant='outline' className='border-primary/30 text-primary text-[11px]'>
                  娱乐体验版
                </Badge>
              </div>
              <p className='text-muted-foreground mt-0.5 text-xs sm:text-sm'>
                本页面专为局域网演示与娱乐体验定制。无论选择哪个档位，均即时展示充值成功动画与订单凭据，对真实账户零扣费、零影响。
              </p>
            </div>
          </div>
        </div>

        {/* Account Info Card */}
        <Card className='border-muted/70 shadow-sm'>
          <CardHeader className='pb-3 pt-4 sm:pb-4 sm:pt-5'>
            <div className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
              <div className='flex items-center gap-3'>
                <div className='flex h-11 w-11 items-center justify-center rounded-full bg-muted font-medium text-muted-foreground'>
                  {currentUser ? (
                    <User className='h-6 w-6 text-foreground' />
                  ) : (
                    <User className='h-6 w-6 text-muted-foreground' />
                  )}
                </div>
                <div>
                  <div className='flex items-center gap-2'>
                    <span className='font-semibold text-base sm:text-lg'>
                      {currentUser?.username || currentUser?.display_name || '局域网访客用户 (LAN Guest)'}
                    </span>
                    <Badge variant='secondary' className='text-xs font-normal'>
                      {currentUser ? '已登录' : '免登录体验'}
                    </Badge>
                  </div>
                  <p className='text-muted-foreground text-xs'>
                    {currentUser?.email || 'ID: lan-guest-8888 • 局域网直连访问'}
                  </p>
                </div>
              </div>

              <div className='flex items-center justify-between rounded-lg border bg-muted/40 px-4 py-2.5 sm:justify-end sm:gap-4'>
                <div className='text-left sm:text-right'>
                  <div className='text-muted-foreground text-xs'>可用算力额度</div>
                  <div className='text-xl font-bold tracking-tight text-emerald-600 dark:text-emerald-400'>
                    ${currentDisplayBalance}
                  </div>
                </div>
                <div className='flex h-8 w-8 items-center justify-center rounded-full bg-emerald-500/10 text-emerald-600 dark:text-emerald-400'>
                  <Wallet className='h-4 w-4' />
                </div>
              </div>
            </div>
          </CardHeader>
        </Card>

        {/* Recharge Tiers Card */}
        <TitledCard
          title='选择充值档位'
          description='请选择您需要充值的面额，支持大额优惠加赠'
          icon={<CreditCard className='h-4 w-4' />}
          iconTone='primary'
        >
          <div className='grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4'>
            {RECHARGE_TIERS.map((tier) => {
              const isSelected = selectedTier.id === tier.id
              return (
                <div
                  key={tier.id}
                  onClick={() => setSelectedTier(tier)}
                  className={cn(
                    'group relative flex cursor-pointer flex-col justify-between rounded-xl border p-4 transition-all duration-200 hover:shadow-md',
                    isSelected
                      ? 'border-primary bg-primary/[0.04] ring-2 ring-primary/20 shadow-sm dark:bg-primary/[0.08]'
                      : 'border-border/80 bg-card hover:border-primary/50'
                  )}
                >
                  {/* Badge */}
                  {tier.tag && (
                    <div className='absolute -top-2.5 right-3'>
                      <Badge
                        variant={tier.popular ? 'default' : 'secondary'}
                        className={cn(
                          'text-[10px] font-medium tracking-wide shadow-xs',
                          tier.popular
                            ? 'bg-primary text-primary-foreground font-semibold'
                            : 'bg-muted text-muted-foreground'
                        )}
                      >
                        {tier.tag}
                      </Badge>
                    </div>
                  )}

                  <div>
                    <div className='text-muted-foreground text-xs font-medium'>充值面额</div>
                    <div className='mt-1 flex items-baseline gap-1'>
                      <span className='text-2xl font-bold tracking-tight text-foreground sm:text-3xl'>
                        ¥{tier.amount}
                      </span>
                    </div>

                    <div className='mt-2 flex items-center gap-1.5 text-xs font-medium text-emerald-600 dark:text-emerald-400'>
                      <Gift className='h-3.5 w-3.5' />
                      <span>加赠 ¥{tier.bonusCNY} 额度</span>
                    </div>

                    <p className='text-muted-foreground mt-2 text-xs leading-relaxed'>
                      {tier.description}
                    </p>
                  </div>

                  <div className='mt-4 flex items-center justify-between border-t pt-3'>
                    <span className='text-muted-foreground text-xs'>
                      实得额度: <strong className='text-foreground'>${tier.quotaUSD}</strong>
                    </span>
                    <div
                      className={cn(
                        'flex h-5 w-5 items-center justify-center rounded-full border transition-colors',
                        isSelected
                          ? 'border-primary bg-primary text-primary-foreground'
                          : 'border-muted-foreground/30 text-transparent'
                      )}
                    >
                      <CheckCircle2 className='h-3.5 w-3.5' />
                    </div>
                  </div>
                </div>
              )
            })}
          </div>
        </TitledCard>

        {/* Payment Methods & Action Card */}
        <TitledCard
          title='支付方式'
          description='支持多种官方直连与便捷移动支付'
          icon={<ShieldCheck className='h-4 w-4' />}
          iconTone='success'
        >
          <div className='space-y-6'>
            <div className='grid grid-cols-2 gap-3 sm:grid-cols-4'>
              {PAYMENT_METHODS.map((method) => {
                const isSelected = selectedMethod === method.type
                return (
                  <button
                    key={method.type}
                    type='button'
                    onClick={() => setSelectedMethod(method.type)}
                    className={cn(
                      'relative flex items-center justify-between rounded-lg border p-3 text-left transition-all',
                      isSelected
                        ? 'border-primary bg-primary/[0.04] ring-1 ring-primary dark:bg-primary/[0.08]'
                        : 'border-border/80 bg-card hover:bg-muted/40'
                    )}
                  >
                    <div className='flex items-center gap-2.5'>
                      <div
                        className={cn(
                          'flex h-7 w-7 items-center justify-center rounded-md border bg-background font-bold shadow-xs',
                          method.iconColor
                        )}
                      >
                        {method.type === 'wechat' && <span className='text-xs font-black'>微</span>}
                        {method.type === 'alipay' && <span className='text-xs font-black'>支</span>}
                        {method.type === 'unionpay' && <CreditCard className='h-4 w-4' />}
                        {method.type === 'applepay' && <span className='text-xs font-black'></span>}
                      </div>
                      <span className='text-sm font-medium'>{method.name}</span>
                    </div>

                    {method.badgeText && (
                      <Badge variant='outline' className='text-[10px] px-1.5 py-0'>
                        {method.badgeText}
                      </Badge>
                    )}
                  </button>
                )
              })}
            </div>

            <Separator />

            {/* Summary & Submit */}
            <div className='flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between'>
              <div className='space-y-1'>
                <div className='text-muted-foreground text-xs'>本次应付金额</div>
                <div className='flex items-baseline gap-2'>
                  <span className='text-3xl font-extrabold tracking-tight text-primary'>
                    ¥{selectedTier.amount}.00
                  </span>
                  <span className='text-muted-foreground text-xs'>
                    (实际到账 ${selectedTier.quotaUSD} + 赠送 ¥{selectedTier.bonusCNY})
                  </span>
                </div>
              </div>

              <Button
                size='lg'
                onClick={handleRecharge}
                disabled={isProcessing}
                className='h-12 min-w-48 gap-2 rounded-xl text-base font-semibold shadow-md transition-all hover:scale-[1.01]'
              >
                {isProcessing ? (
                  <>
                    <Loader2 className='h-5 w-5 animate-spin' />
                    <span>正在发起安全支付...</span>
                  </>
                ) : (
                  <>
                    <Zap className='h-5 w-5 fill-current' />
                    <span>立即充值 ¥{selectedTier.amount}</span>
                  </>
                )}
              </Button>
            </div>
          </div>
        </TitledCard>
      </div>

      {/* Recharge Success Dialog */}
      <Dialog open={successDialogOpen} onOpenChange={setSuccessDialogOpen}>
        <DialogContent className='sm:max-w-md'>
          <DialogHeader className='text-center sm:text-center'>
            <div className='mx-auto mb-3 flex h-14 w-14 items-center justify-center rounded-full bg-emerald-500/15 text-emerald-600 dark:text-emerald-400'>
              <CheckCircle2 className='h-8 w-8' />
            </div>
            <DialogTitle className='text-2xl font-bold tracking-tight text-foreground'>
              充值成功！
            </DialogTitle>
            <DialogDescription className='text-muted-foreground text-sm'>
              您的额度已成功入账，可立即开始调用 New API 服务
            </DialogDescription>
          </DialogHeader>

          {receipt && (
            <div className='space-y-3 rounded-xl border bg-muted/30 p-4 text-xs sm:text-sm'>
              <div className='flex items-center justify-between'>
                <span className='text-muted-foreground'>订单编号</span>
                <span className='font-mono font-medium'>{receipt.orderId}</span>
              </div>
              <div className='flex items-center justify-between'>
                <span className='text-muted-foreground'>支付方式</span>
                <span className='font-medium'>{receipt.paymentMethod.name}</span>
              </div>
              <div className='flex items-center justify-between'>
                <span className='text-muted-foreground'>充值面额</span>
                <span className='font-semibold text-foreground'>¥{receipt.tier.amount}.00</span>
              </div>
              <div className='flex items-center justify-between'>
                <span className='text-muted-foreground'>额外赠送</span>
                <span className='font-semibold text-emerald-600 dark:text-emerald-400'>
                  +¥{receipt.tier.bonusCNY}.00
                </span>
              </div>
              <div className='flex items-center justify-between'>
                <span className='text-muted-foreground'>交易时间</span>
                <span className='text-muted-foreground'>{receipt.timestamp}</span>
              </div>
              <Separator />
              <div className='flex items-center justify-between font-medium'>
                <span>到账状态</span>
                <Badge variant='outline' className='border-emerald-500/40 text-emerald-600 dark:text-emerald-400'>
                  已入账 (即时到账)
                </Badge>
              </div>
            </div>
          )}

          <div className='rounded-lg border border-amber-500/20 bg-amber-500/5 p-3 text-center text-xs text-muted-foreground'>
            ℹ️ 本次充值为局域网娱乐模拟体验，未发生实际资金划扣，账号数据安全无损。
          </div>

          <DialogFooter className='sm:justify-stretch'>
            <Button
              className='w-full'
              size='lg'
              onClick={() => setSuccessDialogOpen(false)}
            >
              完成并继续
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </PublicLayout>
  )
}
