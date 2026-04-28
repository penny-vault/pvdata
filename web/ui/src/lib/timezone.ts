// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
import { ref, watch } from 'vue'

export type DisplayTz = 'ET' | 'local'

const STORAGE_KEY = 'pvdata.displayTz'

function loadInitial(): DisplayTz {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    return stored === 'local' ? 'local' : 'ET'
  } catch {
    return 'ET'
  }
}

export const displayTz = ref<DisplayTz>(loadInitial())

watch(displayTz, (v) => {
  try { localStorage.setItem(STORAGE_KEY, v) } catch { /* ignore */ }
})

export function toggleTz() {
  displayTz.value = displayTz.value === 'ET' ? 'local' : 'ET'
}

export interface FormatOpts {
  date?: boolean
  time?: boolean
  showTz?: boolean
}

export function formatTimestamp(input: string | Date | null | undefined, opts: FormatOpts = {}): string {
  if (!input) return '--'
  if (typeof input === 'string' && input.startsWith('0001')) return '--'
  const d = input instanceof Date ? input : new Date(input)
  if (isNaN(d.getTime())) return '--'

  const date = opts.date ?? true
  const time = opts.time ?? true
  const showTz = opts.showTz ?? true

  const fmt: Intl.DateTimeFormatOptions = {}
  if (date) { fmt.year = 'numeric'; fmt.month = 'short'; fmt.day = 'numeric' }
  if (time) { fmt.hour = '2-digit'; fmt.minute = '2-digit' }
  if (showTz && time) fmt.timeZoneName = 'short'
  if (displayTz.value === 'ET') fmt.timeZone = 'America/New_York'

  return new Intl.DateTimeFormat(undefined, fmt).format(d)
}
