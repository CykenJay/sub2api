<template>
  <div v-if="eligible" class="flex min-w-[6rem] flex-col items-start">
    <div class="flex h-6 items-center gap-1">
      <HelpTooltip class="-ml-1" width-class="w-max max-w-[calc(100vw-2rem)]" data-testid="model-degradation-details">
        <template #trigger>
          <span
            class="cursor-help whitespace-nowrap border-b border-dotted border-gray-300 text-sm font-medium dark:border-dark-600"
            :class="statusClass"
            data-testid="model-degradation-status"
          >
            {{ statusLabel }}
          </span>
        </template>
        <div class="space-y-1">
          <p>{{ t('admin.accounts.modelDegradation.expected', { model: snapshot?.expected_model || configuredModel }) }}</p>
          <template v-if="snapshot">
            <p v-if="snapshot.top_model">
              {{ t('admin.accounts.modelDegradation.topCandidate', { model: snapshot.top_model, probability: formatProbability(snapshot.top_probability) }) }}
            </p>
            <p v-if="snapshot.top_model && snapshot.top_model !== snapshot.expected_model">
              {{ t('admin.accounts.modelDegradation.expectedProbability', { model: snapshot.expected_model, probability: formatProbability(snapshot.expected_probability) }) }}
            </p>
            <p v-if="candidateSummary">{{ t('admin.accounts.modelDegradation.candidates', { value: candidateSummary }) }}</p>
            <p v-if="snapshot.requested_numbers">
              {{ t('admin.accounts.modelDegradation.parsed', { parsed: snapshot.parsed_numbers ?? 0, requested: snapshot.requested_numbers }) }}
            </p>
            <p v-if="snapshot.attempts" data-testid="model-degradation-attempts">
              {{ t('admin.accounts.modelDegradation.attempts', { count: snapshot.attempts }) }}
            </p>
            <p v-if="snapshot.consecutive_degraded > 0">
              {{ t('admin.accounts.modelDegradation.consecutive', { count: snapshot.consecutive_degraded }) }}
            </p>
            <p v-if="pausedActive" class="text-red-400">
              {{ t('admin.accounts.modelDegradation.pausedUntil', { value: formatDate(snapshot.paused_until) }) }}
            </p>
            <p v-if="probeResumeActive" data-testid="model-degradation-probe-resume">
              {{ t('admin.accounts.modelDegradation.probeResumeAt', { value: formatTimestamp(schedule.probeResumeAt) }) }}
            </p>
            <p v-if="snapshot.status === 'failed' && snapshot.last_error" class="max-w-xs break-words">
              {{ t('admin.accounts.modelDegradation.lastError', { value: snapshot.last_error }) }}
            </p>
            <p>{{ t('admin.accounts.modelDegradation.lastAttemptAt', { value: formatDate(snapshot.last_attempt_at) }) }}</p>
            <p v-if="schedule.nextProbeAt != null" data-testid="model-degradation-next-probe">
              {{
                t('admin.accounts.modelDegradation.nextProbeAt', {
                  value: schedule.nextProbeAt > Date.now()
                    ? formatTimestamp(schedule.nextProbeAt)
                    : t('admin.accounts.modelDegradation.nextProbeSoon')
                })
              }}
            </p>
          </template>
          <p class="mt-2 border-t border-white/15 pt-2">
            {{
              t('admin.accounts.modelDegradation.intervalState', {
                value: intervalMinutes > 0
                  ? t('admin.accounts.modelDegradation.intervalMinutes', { count: intervalMinutes })
                  : t('admin.accounts.modelDegradation.disabled')
              })
            }}
          </p>
          <p class="text-gray-400">{{ t('admin.accounts.modelDegradation.disclaimer', { percent: snapshot ? snapshot.min_confidence_percent || MODEL_DEGRADATION_DEFAULT_CONFIDENCE : settings.minConfidencePercent }) }}</p>
        </div>
      </HelpTooltip>
      <button
        type="button"
        class="inline-flex h-6 w-6 flex-shrink-0 items-center justify-center rounded text-blue-600 transition-colors hover:bg-blue-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-900/30"
        :disabled="probing"
        :aria-label="t('admin.accounts.modelDegradation.manualProbe')"
        :title="t('admin.accounts.modelDegradation.manualProbe')"
        data-testid="model-degradation-probe"
        @click="$emit('probe')"
      >
        <Icon name="refresh" size="xs" :class="{ 'animate-spin': probing }" />
      </button>
    </div>
    <div class="flex items-center gap-1.5">
      <button
        type="button"
        role="switch"
        :aria-checked="pauseEnabled"
        :aria-label="t('admin.accounts.modelDegradation.pauseShort')"
        :disabled="pauseSaving"
        class="relative inline-flex h-4 w-7 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 dark:focus:ring-offset-dark-800"
        :class="[pauseEnabled ? 'bg-primary-500 hover:bg-primary-600' : 'bg-gray-200 hover:bg-gray-300 dark:bg-dark-600 dark:hover:bg-dark-500']"
        data-testid="model-degradation-pause-toggle"
        @click="$emit('toggle-pause', !pauseEnabled)"
      >
        <span
          class="pointer-events-none inline-block h-3 w-3 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out"
          :class="[pauseEnabled ? 'translate-x-3' : 'translate-x-0']"
        />
      </button>
      <span class="whitespace-nowrap text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.modelDegradation.pauseShort') }}
      </span>
    </div>
    <span
      v-if="pausedActive"
      class="mt-0.5 whitespace-nowrap text-[10px] font-medium text-red-600 dark:text-red-400"
      data-testid="model-degradation-paused"
    >
      {{ t('admin.accounts.modelDegradation.paused') }}
    </span>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime } from '@/utils/format'
import type { Account, ModelDegradationProbeSnapshot } from '@/types'
import {
  MODEL_DEGRADATION_DEFAULT_CONFIDENCE,
  defaultProbeModel,
  isProbeAccount,
  computeModelDegradationSchedule,
  readModelDegradationSettings
} from './modelDegradationProbe'

const props = defineProps<{
  account: Account
  probing?: boolean
  pauseSaving?: boolean
}>()

defineEmits<{
  (event: 'probe'): void
  (event: 'toggle-pause', enabled: boolean): void
}>()

const { t } = useI18n()

const settings = computed(() => readModelDegradationSettings(props.account.extra))
// 尚无检测结果时展示账号当前选择的检测模型。
const configuredModel = computed(() => settings.value.model || defaultProbeModel(props.account.platform))
const pauseEnabled = computed(() => settings.value.pauseEnabled)

const eligible = computed(() =>
  isProbeAccount(props.account.platform, props.account.type)
)
const snapshot = computed<ModelDegradationProbeSnapshot | undefined>(() => {
  const value = props.account.extra?.model_degradation_probe
  return value && ['normal', 'degraded', 'failed'].includes(value.status) ? value : undefined
})
const intervalMinutes = computed(() => {
  const value = Number(props.account.extra?.model_degradation_probe_interval_minutes ?? 0)
  return Number.isFinite(value) && value > 0 ? Math.floor(value) : 0
})

const isFuture = (value?: string) => hasTime(value) && Date.parse(value as string) > Date.now()
const pausedActive = computed(() => isFuture(snapshot.value?.paused_until))
const schedule = computed(() => computeModelDegradationSchedule(props.account.extra))
const probeResumeActive = computed(() => (schedule.value.probeResumeAt ?? 0) > Date.now())

const statusLabel = computed(() => {
  switch (snapshot.value?.status) {
    case 'normal':
      return t('admin.accounts.modelDegradation.normal')
    case 'degraded':
      return t('admin.accounts.modelDegradation.degraded')
    case 'failed':
      return t('admin.accounts.modelDegradation.failed')
    default:
      return intervalMinutes.value > 0
        ? t('admin.accounts.modelDegradation.notProbed')
        : t('admin.accounts.modelDegradation.disabled')
  }
})
const statusClass = computed(() => {
  switch (snapshot.value?.status) {
    case 'normal':
      return 'text-emerald-600 dark:text-emerald-400'
    case 'degraded':
      return 'text-red-600 dark:text-red-400'
    case 'failed':
      return 'text-amber-600 dark:text-amber-400'
    default:
      return 'text-gray-400 dark:text-gray-500'
  }
})
const candidateSummary = computed(() =>
  (snapshot.value?.candidates ?? [])
    .map(candidate => `${candidate.model} ${formatProbability(candidate.probability)}`)
    .join(' / ')
)

function formatProbability(value?: number) {
  return typeof value === 'number' && Number.isFinite(value) ? `${(value * 100).toFixed(1)}%` : '-'
}

function hasTime(value?: string) {
  return typeof value === 'string' && Number.isFinite(Date.parse(value)) && !value.startsWith('0001-')
}

function formatTimestamp(value: number | null) {
  return value == null ? '-' : formatDateTime(new Date(value))
}

function formatDate(value?: string) {
  return hasTime(value) ? formatDateTime(value as string) : '-'
}
</script>
