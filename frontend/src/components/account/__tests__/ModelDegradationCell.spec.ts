import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ModelDegradationCell from '../ModelDegradationCell.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key
    })
  }
})

function mountCell(account: Partial<Account>) {
  return mount(ModelDegradationCell, {
    props: { account: { id: 1, platform: 'openai', type: 'oauth', ...account } as Account },
    global: {
      stubs: {
        HelpTooltip: { template: '<div><slot name="trigger" /><slot /></div>' },
        Icon: true
      }
    }
  })
}

describe('ModelDegradationCell', () => {
  it('非 OpenAI 账号显示占位', () => {
    const wrapper = mountCell({ platform: 'anthropic' })
    expect(wrapper.find('[data-testid="model-degradation-status"]').exists()).toBe(false)
    expect(wrapper.text()).toBe('-')
  })

  it('暂停已过期时不显示已暂停调度', () => {
    const wrapper = mountCell({
      extra: {
        model_degradation_probe: {
          status: 'degraded',
          expected_model: 'gpt-6-astra',
          consecutive_degraded: 1,
          paused_until: '2020-01-01T00:00:00Z',
          last_attempt_at: '2020-01-01T00:00:00Z',
          next_probe_at: '2020-01-01T00:00:00Z'
        }
      }
    })
    expect(wrapper.find('[data-testid="model-degradation-paused"]').exists()).toBe(false)
  })

  it('未配置间隔且无结果时显示未开启', () => {
    const wrapper = mountCell({ extra: {} })
    expect(wrapper.get('[data-testid="model-degradation-status"]').text()).toBe('admin.accounts.modelDegradation.disabled')
  })

  it('疑似降智时显示第一候选与暂停状态，并可触发立即检测', async () => {
    const wrapper = mountCell({
      extra: {
        model_degradation_probe_interval_minutes: 30,
        model_degradation_probe_cooldown_minutes: 120,
        model_degradation_probe: {
          status: 'degraded',
          expected_model: 'gpt-6-astra',
          top_model: 'gpt-6-luna',
          top_probability: 0.912,
          expected_probability: 0.05,
          consecutive_degraded: 2,
          paused_until: new Date(Date.now() + 3_600_000).toISOString(),
          last_attempt_at: new Date(Date.now() - 600_000).toISOString(),
          next_probe_at: '2026-11-03T10:30:00Z'
        }
      }
    })
    const status = wrapper.get('[data-testid="model-degradation-status"]')
    expect(status.text()).toBe('admin.accounts.modelDegradation.degraded')
    expect(status.classes()).toContain('text-red-600')
    expect(wrapper.find('[data-testid="model-degradation-paused"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="model-degradation-probe-resume"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="model-degradation-next-probe"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('"model":"gpt-6-luna","probability":"91.2%"')

    await wrapper.get('[data-testid="model-degradation-probe"]').trigger('click')
    expect(wrapper.emitted('probe')).toHaveLength(1)
  })
})
