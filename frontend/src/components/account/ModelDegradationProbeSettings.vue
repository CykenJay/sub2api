<template>
  <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
    <div v-for="field in fields" :key="field.name">
      <label class="input-label">{{ t(field.label) }}</label>
      <input
        :value="modelValue[field.name]"
        type="number"
        min="0"
        :max="field.maximum"
        step="1"
        class="input"
        :disabled="field.name === 'pauseMinutes' && !modelValue.pauseThreshold"
        :data-testid="field.testId"
        @input="update(field.name, $event)"
      />
      <p class="input-hint">{{ t(field.hint) }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import {
  MODEL_DEGRADATION_MAX_MINUTES,
  MODEL_DEGRADATION_MAX_PAUSE_THRESHOLD,
  type ModelDegradationSettings
} from './modelDegradationProbe'

const props = defineProps<{
  modelValue: ModelDegradationSettings
}>()

const emit = defineEmits<{
  (event: 'update:modelValue', value: ModelDegradationSettings): void
}>()

const { t } = useI18n()

const fields: {
  name: keyof ModelDegradationSettings
  label: string
  hint: string
  maximum: number
  testId: string
}[] = [
  {
    name: 'intervalMinutes',
    label: 'admin.accounts.modelDegradation.interval',
    hint: 'admin.accounts.modelDegradation.intervalHint',
    maximum: MODEL_DEGRADATION_MAX_MINUTES,
    testId: 'model-degradation-interval'
  },
  {
    name: 'cooldownMinutes',
    label: 'admin.accounts.modelDegradation.cooldown',
    hint: 'admin.accounts.modelDegradation.cooldownHint',
    maximum: MODEL_DEGRADATION_MAX_MINUTES,
    testId: 'model-degradation-cooldown'
  },
  {
    name: 'pauseThreshold',
    label: 'admin.accounts.modelDegradation.pauseThreshold',
    hint: 'admin.accounts.modelDegradation.pauseThresholdHint',
    maximum: MODEL_DEGRADATION_MAX_PAUSE_THRESHOLD,
    testId: 'model-degradation-pause-threshold'
  },
  {
    name: 'pauseMinutes',
    label: 'admin.accounts.modelDegradation.pauseMinutes',
    hint: 'admin.accounts.modelDegradation.pauseMinutesHint',
    maximum: MODEL_DEGRADATION_MAX_MINUTES,
    testId: 'model-degradation-pause-minutes'
  }
]

function update(name: keyof ModelDegradationSettings, event: Event) {
  const raw = (event.target as HTMLInputElement).value
  const value = raw.trim() === '' ? null : Number(raw)
  emit('update:modelValue', { ...props.modelValue, [name]: Number.isFinite(value) ? value : null })
}
</script>
