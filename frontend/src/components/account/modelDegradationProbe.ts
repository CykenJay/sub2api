import { getModelDegradationProbeModels } from '@/api/admin/accounts'

// 模型降智检测的账号级配置（accounts.extra），与后端 model_degradation_probe.go 保持一致。
export const MODEL_DEGRADATION_DEFAULT_CONFIDENCE = 98
export const MODEL_DEGRADATION_DEFAULT_MODEL = 'gpt-6.2-astra'
export const MODEL_DEGRADATION_MAX_MINUTES = 7 * 24 * 60
export const MODEL_DEGRADATION_MAX_PAUSE_THRESHOLD = 100
export const MODEL_DEGRADATION_DEFAULT_PAUSE_MINUTES = 60

export function defaultProbeModel(platform?: string): string {
  return platform === 'anthropic' ? 'claude-opus-5-5' : MODEL_DEGRADATION_DEFAULT_MODEL
}

export function isProbeAccount(platform: string, type: string): boolean {
  if (platform === 'anthropic') return ['oauth', 'setup-token', 'apikey', 'bedrock', 'service_account'].includes(type)
  return platform === 'openai' && (type === 'oauth' || type === 'apikey')
}

const PAUSE_ENABLED_KEY = 'model_degradation_probe_pause_enabled'
const MODEL_KEY = 'model_degradation_probe_model'

export interface ModelDegradationSettings {
  // 检测的模型，'' 表示默认模型。
  model: string
  minConfidencePercent: number | null
  intervalMinutes: number | null
  // 疑似降智后是否暂停调度；关闭时保留阈值与时长，重新勾选即可沿用。
  pauseEnabled: boolean
  pauseThreshold: number | null
  pauseMinutes: number | null
  cooldownMinutes: number | null
}

type NumericSettingField = Exclude<keyof ModelDegradationSettings, 'model' | 'pauseEnabled'>

const SETTING_KEYS: Record<NumericSettingField, { key: string; maximum: number }> = {
  minConfidencePercent: { key: 'model_degradation_probe_min_confidence_percent', maximum: 100 },
  intervalMinutes: { key: 'model_degradation_probe_interval_minutes', maximum: MODEL_DEGRADATION_MAX_MINUTES },
  pauseThreshold: { key: 'model_degradation_probe_pause_threshold', maximum: MODEL_DEGRADATION_MAX_PAUSE_THRESHOLD },
  pauseMinutes: { key: 'model_degradation_probe_pause_minutes', maximum: MODEL_DEGRADATION_MAX_MINUTES },
  cooldownMinutes: { key: 'model_degradation_probe_cooldown_minutes', maximum: MODEL_DEGRADATION_MAX_MINUTES }
}

function normalizeSetting(value: unknown, maximum: number): number {
  const number = Number(value)
  if (!Number.isFinite(number) || number <= 0) return 0
  return Math.min(Math.floor(number), maximum)
}

export function defaultModelDegradationSettings(): ModelDegradationSettings {
  return {
    model: '',
    minConfidencePercent: MODEL_DEGRADATION_DEFAULT_CONFIDENCE,
    intervalMinutes: 0,
    pauseEnabled: false,
    pauseThreshold: 1,
    pauseMinutes: MODEL_DEGRADATION_DEFAULT_PAUSE_MINUTES,
    cooldownMinutes: 0
  }
}

export function readModelDegradationSettings(extra?: Record<string, unknown> | null): ModelDegradationSettings {
  const settings = defaultModelDegradationSettings()
  for (const [field, { key, maximum }] of Object.entries(SETTING_KEYS)) {
    settings[field as NumericSettingField] = normalizeSetting(extra?.[key], maximum)
  }
  if (!settings.minConfidencePercent) settings.minConfidencePercent = MODEL_DEGRADATION_DEFAULT_CONFIDENCE
  const model = extra?.[MODEL_KEY]
  settings.model = typeof model === 'string' ? model.trim() : ''
  // 未设置开关时沿用旧语义：阈值大于 0 即开启暂停。
  const pauseEnabled = extra?.[PAUSE_ENABLED_KEY]
  settings.pauseEnabled = typeof pauseEnabled === 'boolean' ? pauseEnabled : Boolean(settings.pauseThreshold)
  // 开启暂停时阈值 0 按 1 处理，与后端一致。
  if (!settings.pauseThreshold) settings.pauseThreshold = 1
  if (!settings.pauseMinutes) settings.pauseMinutes = MODEL_DEGRADATION_DEFAULT_PAUSE_MINUTES
  return settings
}

// 批量更新是浅合并，关闭时也要显式写入 0 / false 才能覆盖旧值。
export function buildModelDegradationExtra(settings: ModelDegradationSettings): Record<string, string | number | boolean> {
  const extra: Record<string, string | number | boolean> = {
    [MODEL_KEY]: settings.model.trim(),
    [PAUSE_ENABLED_KEY]: settings.pauseEnabled
  }
  for (const [field, { key, maximum }] of Object.entries(SETTING_KEYS)) {
    extra[key] = normalizeSetting(settings[field as NumericSettingField], maximum)
  }
  extra[SETTING_KEYS.minConfidencePercent.key] ||= MODEL_DEGRADATION_DEFAULT_CONFIDENCE
  return extra
}

export interface ModelDegradationSchedule {
  // 疑似降智后的停检截止时间（毫秒时间戳），未处于停检规则下为 null。
  probeResumeAt: number | null
  // 下次自动检测时间，未开启检测或尚无检测记录时为 null。
  nextProbeAt: number | null
}

// 与后端 modelDegradationProbeNextAt 一致：按上次检测时间与当前配置实时计算，修改配置立即反映。
export function computeModelDegradationSchedule(extra?: Record<string, unknown> | null): ModelDegradationSchedule {
  const snapshot = extra?.model_degradation_probe as { status?: string; last_attempt_at?: string } | undefined
  const lastAttempt = Date.parse(snapshot?.last_attempt_at ?? '')
  if (!snapshot || !Number.isFinite(lastAttempt) || lastAttempt <= 0) {
    return { probeResumeAt: null, nextProbeAt: null }
  }
  const { intervalMinutes, cooldownMinutes } = readModelDegradationSettings(extra)
  const probeResumeAt =
    snapshot.status === 'degraded' && cooldownMinutes ? lastAttempt + cooldownMinutes * 60_000 : null
  const nextProbeAt = intervalMinutes ? probeResumeAt ?? lastAttempt + intervalMinutes * 60_000 : null
  return { probeResumeAt, nextProbeAt }
}

let probeModelsPromise: Promise<string[]> | null = null

// 加载可选的检测模型并在页面内缓存；失败时返回空列表且不缓存，下次挂载重新请求。
export function loadModelDegradationProbeModels(): Promise<string[]> {
  probeModelsPromise ??= Promise.resolve()
    .then(() => getModelDegradationProbeModels())
    .then((result) => result.models)
    .catch(() => {
      probeModelsPromise = null
      return []
    })
  return probeModelsPromise
}
