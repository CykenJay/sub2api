// 模型降智检测的账号级配置（accounts.extra），与后端 model_degradation_probe.go 保持一致。
export const MODEL_DEGRADATION_MAX_MINUTES = 7 * 24 * 60
export const MODEL_DEGRADATION_MAX_PAUSE_THRESHOLD = 100
export const MODEL_DEGRADATION_DEFAULT_PAUSE_MINUTES = 60

export interface ModelDegradationSettings {
  intervalMinutes: number | null
  pauseThreshold: number | null
  pauseMinutes: number | null
  cooldownMinutes: number | null
}

const SETTING_KEYS: Record<keyof ModelDegradationSettings, { key: string; maximum: number }> = {
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
  return { intervalMinutes: 0, pauseThreshold: 0, pauseMinutes: MODEL_DEGRADATION_DEFAULT_PAUSE_MINUTES, cooldownMinutes: 0 }
}

export function readModelDegradationSettings(extra?: Record<string, unknown> | null): ModelDegradationSettings {
  const settings = {} as ModelDegradationSettings
  for (const [field, { key, maximum }] of Object.entries(SETTING_KEYS)) {
    settings[field as keyof ModelDegradationSettings] = normalizeSetting(extra?.[key], maximum)
  }
  if (!settings.pauseMinutes) settings.pauseMinutes = MODEL_DEGRADATION_DEFAULT_PAUSE_MINUTES
  return settings
}

// 批量更新是浅合并，关闭时也要显式写 0 才能覆盖旧值。
export function buildModelDegradationExtra(settings: ModelDegradationSettings): Record<string, number> {
  const extra: Record<string, number> = {}
  for (const [field, { key, maximum }] of Object.entries(SETTING_KEYS)) {
    extra[key] = normalizeSetting(settings[field as keyof ModelDegradationSettings], maximum)
  }
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
