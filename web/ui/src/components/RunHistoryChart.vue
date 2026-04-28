<script setup lang="ts">
import { computed } from 'vue'
import Chart from 'primevue/chart'
import { displayTz, formatTimestamp } from '@/lib/timezone'

interface Run {
  start_time: string
  num_observations: number
  status: string
}

const props = defineProps<{
  runs: Run[]
}>()

const sortedRuns = computed(() => {
  if (!props.runs || props.runs.length === 0) return []
  return [...props.runs].sort(
    (a, b) => new Date(a.start_time).getTime() - new Date(b.start_time).getTime()
  )
})

function cssVar(name: string, fallback: string): string {
  if (typeof window === 'undefined') return fallback
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  return v || fallback
}

const chartData = computed(() => {
  void displayTz.value
  const runs = sortedRuns.value
  const labelFmt: Intl.DateTimeFormatOptions = { month: 'short', day: 'numeric' }
  if (displayTz.value === 'ET') labelFmt.timeZone = 'America/New_York'
  const labelFormatter = new Intl.DateTimeFormat(undefined, labelFmt)

  const primary = cssVar('--p-primary-color', '#3b82f6')
  const failed = cssVar('--p-red-400', '#f87171')

  return {
    labels: runs.map((r) => labelFormatter.format(new Date(r.start_time))),
    datasets: [
      {
        label: 'Records',
        data: runs.map((r) => r.num_observations),
        backgroundColor: runs.map((r) =>
          r.status === 'failed' ? failed : primary
        ),
        borderRadius: 2,
        borderSkipped: false,
      },
    ],
  }
})

const chartOptions = computed(() => {
  void displayTz.value
  const runs = sortedRuns.value
  const axisColor = cssVar('--p-text-muted-color', 'rgba(255,255,255,0.5)')
  const gridColor = cssVar('--p-content-border-color', 'rgba(255,255,255,0.1)')

  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { mode: 'nearest', axis: 'x', intersect: false },
    plugins: {
      legend: { display: false },
      tooltip: {
        callbacks: {
          title: (items: any[]) => {
            const idx = items[0]?.dataIndex
            const run = runs[idx]
            if (!run) return ''
            return formatTimestamp(run.start_time)
          },
          label: (item: any) => {
            const run = runs[item.dataIndex]
            const recs = item.parsed.y.toLocaleString()
            return `${recs} records (${run?.status ?? 'unknown'})`
          },
        },
      },
    },
    scales: {
      x: {
        ticks: {
          color: axisColor,
          font: { family: 'ui-monospace, SFMono-Regular, Menlo, monospace', size: 10 },
          maxRotation: 0,
          autoSkip: true,
          autoSkipPadding: 16,
        },
        grid: { display: false },
      },
      y: {
        beginAtZero: true,
        ticks: {
          color: axisColor,
          font: { family: 'ui-monospace, SFMono-Regular, Menlo, monospace', size: 10 },
          callback: (val: number) => Number(val).toLocaleString(),
        },
        grid: { color: gridColor },
      },
    },
  }
})
</script>

<template>
  <div v-if="sortedRuns.length === 0" class="chart-empty">
    <p>No run history available</p>
  </div>
  <div v-else class="chart-container">
    <Chart type="bar" :data="chartData" :options="chartOptions" class="chart-canvas" />
  </div>
</template>

<style scoped>
.chart-container {
  width: 100%;
  max-width: 800px;
  height: 220px;
}
.chart-canvas {
  width: 100%;
  height: 100%;
}
.chart-empty {
  padding: 2rem;
  text-align: center;
  opacity: 0.4;
}
</style>
