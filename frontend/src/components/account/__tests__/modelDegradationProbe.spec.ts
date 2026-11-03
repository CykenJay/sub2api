import { describe, expect, it } from 'vitest'
import { computeModelDegradationSchedule } from '../modelDegradationProbe'

const last = '2026-11-03T11:07:00Z'
const lastMs = Date.parse(last)
const extra = (status: string, settings: Record<string, number>) => ({
  ...settings,
  model_degradation_probe: { status, last_attempt_at: last, next_probe_at: '0001-01-01T00:00:00Z' }
})

describe('computeModelDegradationSchedule', () => {
  it('疑似降智且配置了停检时长时，按停检时长计算下次检测', () => {
    const schedule = computeModelDegradationSchedule(extra('degraded', {
      model_degradation_probe_interval_minutes: 5,
      model_degradation_probe_cooldown_minutes: 60
    }))
    expect(schedule.probeResumeAt).toBe(lastMs + 60 * 60_000)
    expect(schedule.nextProbeAt).toBe(lastMs + 60 * 60_000)
  })

  it('结果正常或未配置停检时按检测间隔计算，忽略快照里过期的 next_probe_at', () => {
    expect(computeModelDegradationSchedule(extra('normal', {
      model_degradation_probe_interval_minutes: 5,
      model_degradation_probe_cooldown_minutes: 60
    }))).toEqual({ probeResumeAt: null, nextProbeAt: lastMs + 5 * 60_000 })
    expect(computeModelDegradationSchedule(extra('degraded', {
      model_degradation_probe_interval_minutes: 5
    }))).toEqual({ probeResumeAt: null, nextProbeAt: lastMs + 5 * 60_000 })
  })

  it('未开启检测或没有检测记录时没有下次检测时间', () => {
    expect(computeModelDegradationSchedule(extra('degraded', { model_degradation_probe_cooldown_minutes: 60 })).nextProbeAt).toBeNull()
    expect(computeModelDegradationSchedule({ model_degradation_probe_interval_minutes: 5 })).toEqual({ probeResumeAt: null, nextProbeAt: null })
  })
})
