import { describe, expect, it } from 'vitest'
import {
  buildModelDegradationExtra,
  computeModelDegradationSchedule,
  readModelDegradationSettings
} from '../modelDegradationProbe'

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

describe('降智后暂停调度开关', () => {
  it('未设置开关时沿用旧语义：阈值大于 0 即开启', () => {
    expect(readModelDegradationSettings({ model_degradation_probe_pause_threshold: 2 })).toMatchObject({
      pauseEnabled: true,
      pauseThreshold: 2
    })
    expect(readModelDegradationSettings({})).toMatchObject({ pauseEnabled: false, pauseThreshold: 1 })
  })

  it('显式关闭时保留阈值，重新勾选可沿用', () => {
    const settings = readModelDegradationSettings({
      model_degradation_probe_pause_enabled: false,
      model_degradation_probe_pause_threshold: 3
    })
    expect(settings).toMatchObject({ pauseEnabled: false, pauseThreshold: 3 })
    expect(buildModelDegradationExtra(settings)).toMatchObject({
      model_degradation_probe_pause_enabled: false,
      model_degradation_probe_pause_threshold: 3
    })
  })
})

describe('检测模型设置', () => {
  it('读取时去掉首尾空白，未设置时为空表示默认模型', () => {
    expect(readModelDegradationSettings({ model_degradation_probe_model: ' gpt-6-luna ' }).model).toBe('gpt-6-luna')
    expect(readModelDegradationSettings({}).model).toBe('')
    expect(readModelDegradationSettings({ model_degradation_probe_model: 42 }).model).toBe('')
  })

  it('保存时总是写入检测模型，空值用于恢复默认', () => {
    const settings = readModelDegradationSettings({ model_degradation_probe_model: 'gpt-6-luna' })
    expect(buildModelDegradationExtra(settings).model_degradation_probe_model).toBe('gpt-6-luna')
    expect(buildModelDegradationExtra({ ...settings, model: '' }).model_degradation_probe_model).toBe('')
  })
})
