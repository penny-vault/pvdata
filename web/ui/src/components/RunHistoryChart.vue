<script setup lang="ts">
import { computed } from 'vue'
import { displayTz } from '@/lib/timezone'

interface Run {
  start_time: string
  num_observations: number
  status: string
}

const props = defineProps<{
  runs: Run[]
}>()

const chartData = computed(() => {
  if (!props.runs || props.runs.length === 0) return null
  const sorted = [...props.runs].sort(
    (a, b) => new Date(a.start_time).getTime() - new Date(b.start_time).getTime()
  )
  const maxVal = Math.max(...sorted.map((r) => r.num_observations), 1)
  const width = 600
  const height = 200
  const barPadding = 2
  const barWidth = Math.max(
    2,
    (width - barPadding * sorted.length) / sorted.length
  )

  const labelFmt: Intl.DateTimeFormatOptions = { month: 'short', day: 'numeric' }
  if (displayTz.value === 'ET') labelFmt.timeZone = 'America/New_York'
  const labelFormatter = new Intl.DateTimeFormat(undefined, labelFmt)

  const bars = sorted.map((run, i) => {
    const barHeight = (run.num_observations / maxVal) * (height - 30)
    return {
      x: i * (barWidth + barPadding),
      y: height - 20 - barHeight,
      width: barWidth,
      height: barHeight,
      color: run.status === 'failed' ? 'var(--p-red-400)' : 'var(--p-primary-color)',
      label: labelFormatter.format(new Date(run.start_time)),
      value: run.num_observations,
      status: run.status,
    }
  })

  const yLabels = [0, Math.round(maxVal / 2), maxVal]

  return { bars, width, height, maxVal, yLabels }
})
</script>

<template>
  <div v-if="!chartData" class="chart-empty">
    <p>No run history available</p>
  </div>
  <div v-else class="chart-container">
    <svg
      :viewBox="`0 0 ${chartData.width + 60} ${chartData.height}`"
      class="chart-svg"
      preserveAspectRatio="xMinYMin meet"
    >
      <!-- Y-axis labels -->
      <text
        v-for="(label, i) in chartData.yLabels"
        :key="'y-' + i"
        :x="55"
        :y="chartData.height - 20 - (label / chartData.maxVal) * (chartData.height - 30) + 4"
        class="chart-axis-label"
        text-anchor="end"
      >
        {{ label.toLocaleString() }}
      </text>

      <!-- Bars -->
      <g transform="translate(60, 0)">
        <rect
          v-for="(bar, i) in chartData.bars"
          :key="'bar-' + i"
          :x="bar.x"
          :y="bar.y"
          :width="bar.width"
          :height="Math.max(bar.height, 1)"
          :fill="bar.color"
          rx="2"
        >
          <title>{{ bar.label }}: {{ bar.value.toLocaleString() }} records ({{ bar.status }})</title>
        </rect>

        <!-- X-axis labels (show a subset to avoid crowding) -->
        <text
          v-for="(bar, i) in chartData.bars"
          v-show="i % Math.max(1, Math.floor(chartData.bars.length / 6)) === 0"
          :key="'x-' + i"
          :x="bar.x + bar.width / 2"
          :y="chartData.height - 4"
          class="chart-axis-label"
          text-anchor="middle"
        >
          {{ bar.label }}
        </text>
      </g>
    </svg>
  </div>
</template>

<style scoped>
.chart-container {
  width: 100%;
  max-width: 800px;
}
.chart-svg {
  width: 100%;
  height: auto;
}
.chart-axis-label {
  fill: currentColor;
  opacity: 0.5;
  font-size: 10px;
  font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
}
.chart-empty {
  padding: 2rem;
  text-align: center;
  opacity: 0.4;
}
</style>
