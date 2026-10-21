import type { TempUnschedulableScope } from '@/types'

export const TEMP_UNSCHED_ENABLED_CREDENTIAL_KEY = 'temp_unschedulable_enabled'
export const TEMP_UNSCHED_RULES_CREDENTIAL_KEY = 'temp_unschedulable_rules'

export interface TempUnschedRuleForm {
  error_code: number | null
  keywords: string
  duration_minutes: number | null
  description: string
  // 作用范围：account 暂停整个账号，model 只冷却本次请求的模型。
  scope: TempUnschedulableScope
}

export interface TempUnschedRulePayload {
  error_code: number
  keywords: string[]
  duration_minutes: number
  description: string
  scope: TempUnschedulableScope
}

export const createEmptyTempUnschedRule = (): TempUnschedRuleForm => ({
  error_code: null,
  keywords: '',
  duration_minutes: 30,
  description: '',
  scope: 'model'
})

export const splitTempUnschedKeywords = (value: string) => {
  return value
    .split(/[,;]/)
    .map((item) => item.trim())
    .filter((item) => item.length > 0)
}

// 丢弃错误码、时长或关键词不完整的规则，与后端匹配条件保持一致。
export const buildTempUnschedRules = (rules: TempUnschedRuleForm[]): TempUnschedRulePayload[] => {
  const out: TempUnschedRulePayload[] = []
  for (const rule of rules) {
    const errorCode = Number(rule.error_code)
    const duration = Number(rule.duration_minutes)
    const keywords = splitTempUnschedKeywords(rule.keywords)
    if (!Number.isFinite(errorCode) || errorCode < 100 || errorCode > 599) {
      continue
    }
    if (!Number.isFinite(duration) || duration <= 0) {
      continue
    }
    if (keywords.length === 0) {
      continue
    }
    out.push({
      error_code: Math.trunc(errorCode),
      keywords,
      duration_minutes: Math.trunc(duration),
      description: rule.description.trim(),
      scope: rule.scope
    })
  }
  return out
}

export interface TempUnschedRulePreset {
  label: string
  rule: TempUnschedRuleForm
}

export const buildTempUnschedPresets = (t: (key: string) => string): TempUnschedRulePreset[] => [
  {
    label: t('admin.accounts.tempUnschedulable.presets.overloadLabel'),
    rule: {
      error_code: 529,
      keywords: 'overloaded, too many',
      duration_minutes: 60,
      description: t('admin.accounts.tempUnschedulable.presets.overloadDesc'),
      scope: 'model'
    }
  },
  {
    label: t('admin.accounts.tempUnschedulable.presets.rateLimitLabel'),
    rule: {
      error_code: 429,
      keywords: 'rate limit, too many requests',
      duration_minutes: 10,
      description: t('admin.accounts.tempUnschedulable.presets.rateLimitDesc'),
      scope: 'model'
    }
  },
  {
    label: t('admin.accounts.tempUnschedulable.presets.unavailableLabel'),
    rule: {
      error_code: 503,
      keywords: 'unavailable, maintenance',
      duration_minutes: 30,
      description: t('admin.accounts.tempUnschedulable.presets.unavailableDesc'),
      scope: 'model'
    }
  }
]
