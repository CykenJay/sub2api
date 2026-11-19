<template>
  <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
    <div class="sm:col-span-2">
      <label id="model-degradation-model-label" class="input-label">
        {{ t('admin.accounts.modelDegradation.model') }}
      </label>
      <Select
        :model-value="modelValue.model"
        :options="modelOptions"
        searchable
        data-testid="model-degradation-model"
        aria-labelledby="model-degradation-model-label"
        @update:model-value="updateModel"
      />
      <p class="input-hint">{{ t('admin.accounts.modelDegradation.modelHint') }}</p>
    </div>
    <template v-for="field in fields" :key="field.name">
      <label
        v-if="field.name === 'pauseThreshold'"
        class="flex cursor-pointer items-start gap-2 sm:col-span-2"
      >
        <input
          :checked="modelValue.pauseEnabled"
          type="checkbox"
          class="mt-0.5 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          data-testid="model-degradation-pause-enabled"
          @change="togglePause"
        />
        <span>
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.accounts.modelDegradation.pauseEnabled') }}
          </span>
          <span class="input-hint block">{{ t('admin.accounts.modelDegradation.pauseEnabledHint') }}</span>
        </span>
      </label>
      <div>
        <label class="input-label">{{ t(field.label) }}</label>
        <input
          :value="modelValue[field.name]"
          type="number"
          :min="field.minimum"
          :max="field.maximum"
          step="1"
          class="input"
          :disabled="field.pauseSetting && !modelValue.pauseEnabled"
          :data-testid="field.testId"
          @input="update(field.name, $event)"
        />
        <p class="input-hint">{{ t(field.hint) }}</p>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import {
  MODEL_DEGRADATION_DEFAULT_MODEL,
  MODEL_DEGRADATION_MAX_MINUTES,
  MODEL_DEGRADATION_MAX_PAUSE_THRESHOLD,
  loadModelDegradationProbeModels,
  type ModelDegradationSettings
} from './modelDegradationProbe'

const props = defineProps<{
  modelValue: ModelDegradationSettings
}>()

const emit = defineEmits<{
  (event: 'update:modelValue', value: ModelDegradationSettings): void
}>()

const { t } = useI18n()

const probeModels = ref<string[]>([])

onMounted(async () => {
  probeModels.value = await loadModelDegradationProbeModels()
})

// 已保存但不在当前指纹库里的模型也保留为选项，避免编辑时被悄悄改掉。
const modelOptions = computed(() => {
  const models = probeModels.value.filter((model) => model !== MODEL_DEGRADATION_DEFAULT_MODEL)
  if (props.modelValue.model && props.modelValue.model !== MODEL_DEGRADATION_DEFAULT_MODEL && !models.includes(props.modelValue.model)) {
    models.unshift(props.modelValue.model)
  }
  return [
    { value: '', label: t('admin.accounts.modelDegradation.defaultModel', { model: MODEL_DEGRADATION_DEFAULT_MODEL }) },
    ...models.map((model) => ({ value: model, label: model }))
  ]
})

type NumericField = Exclude<keyof ModelDegradationSettings, 'model' | 'pauseEnabled'>

const fields: {
  name: NumericField
  label: string
  hint: string
  minimum: number
  maximum: number
  testId: string
  // 仅在开启「降智后暂停调度」时可编辑。
  pauseSetting?: boolean
}[] = [
  {
    name: 'intervalMinutes',
    label: 'admin.accounts.modelDegradation.interval',
    hint: 'admin.accounts.modelDegradation.intervalHint',
    minimum: 0,
    maximum: MODEL_DEGRADATION_MAX_MINUTES,
    testId: 'model-degradation-interval'
  },
  {
    name: 'cooldownMinutes',
    label: 'admin.accounts.modelDegradation.cooldown',
    hint: 'admin.accounts.modelDegradation.cooldownHint',
    minimum: 0,
    maximum: MODEL_DEGRADATION_MAX_MINUTES,
    testId: 'model-degradation-cooldown'
  },
  {
    name: 'pauseThreshold',
    label: 'admin.accounts.modelDegradation.pauseThreshold',
    hint: 'admin.accounts.modelDegradation.pauseThresholdHint',
    minimum: 1,
    maximum: MODEL_DEGRADATION_MAX_PAUSE_THRESHOLD,
    testId: 'model-degradation-pause-threshold',
    pauseSetting: true
  },
  {
    name: 'pauseMinutes',
    label: 'admin.accounts.modelDegradation.pauseMinutes',
    hint: 'admin.accounts.modelDegradation.pauseMinutesHint',
    minimum: 1,
    maximum: MODEL_DEGRADATION_MAX_MINUTES,
    testId: 'model-degradation-pause-minutes',
    pauseSetting: true
  }
]

function updateModel(value: string | number | boolean | null) {
  const model = typeof value === 'string' ? value : ''
  emit('update:modelValue', { ...props.modelValue, model: model === MODEL_DEGRADATION_DEFAULT_MODEL ? '' : model })
}

function togglePause(event: Event) {
  emit('update:modelValue', { ...props.modelValue, pauseEnabled: (event.target as HTMLInputElement).checked })
}

function update(name: NumericField, event: Event) {
  const raw = (event.target as HTMLInputElement).value
  const value = raw.trim() === '' ? null : Number(raw)
  emit('update:modelValue', { ...props.modelValue, [name]: Number.isFinite(value) ? value : null })
}
</script>
