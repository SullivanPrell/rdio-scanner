<script setup lang="ts">
import type { RdioConfig, RdioListResult, RdioSearchOptions } from '~/composables/useRdioScanner'

const props = defineProps<{
  config: RdioConfig | null
  listResult: RdioListResult | null
}>()

const emit = defineEmits<{
  search: [opts: RdioSearchOptions]
  playCall: [id: number]
  close: []
}>()

const opts = ref<RdioSearchOptions>({ sort: -1, limit: 50 })

const systemOptions = computed(() =>
  props.config?.systems.map(s => ({ label: s.label, value: s.id })) ?? []
)

const talkgroupOptions = computed(() => {
  if (!opts.value.system) return []
  const sys = props.config?.systems.find(s => s.id === opts.value.system)
  return sys?.talkgroups.map(tg => ({ label: `${tg.id} – ${tg.name}`, value: tg.id })) ?? []
})

// The native date input yields 'YYYY-MM-DD', but the server parses the date
// filter as RFC3339 (call.go fromMap) and silently drops anything else. Send a
// UTC midnight timestamp so the day filter actually applies.
const doSearch = () => emit('search', {
  ...opts.value,
  date: opts.value.date ? `${opts.value.date}T00:00:00Z` : undefined,
})

const formatDateTime = (iso: string) =>
  new Date(iso).toLocaleString('en-US', { dateStyle: 'short', timeStyle: 'medium' })

watch(() => opts.value.system, () => { opts.value.talkgroup = undefined })
</script>

<template>
  <div class="flex flex-col h-full">
    <div class="flex items-center justify-between px-4 py-3 border-b border-neutral-800">
      <span class="font-semibold text-sm">Search Calls</span>
      <UButton icon="i-heroicons-x-mark" variant="ghost" size="xs" @click="emit('close')" />
    </div>

    <!-- Filters -->
    <div class="p-3 space-y-2 border-b border-neutral-800">
      <div class="grid grid-cols-2 gap-2">
        <USelect
          v-model="opts.system"
          :items="systemOptions"
          placeholder="All systems"
          size="sm"
        />
        <USelect
          v-model="opts.talkgroup"
          :items="talkgroupOptions"
          placeholder="All talkgroups"
          size="sm"
          :disabled="!opts.system"
        />
      </div>
      <div class="grid grid-cols-2 gap-2">
        <UInput v-model="opts.date" type="date" size="sm" placeholder="Date" />
        <USelect
          v-model="opts.sort"
          :items="[{ label: 'Newest first', value: -1 }, { label: 'Oldest first', value: 1 }]"
          size="sm"
        />
      </div>
      <UButton block size="sm" icon="i-heroicons-magnifying-glass" @click="doSearch">
        Search
      </UButton>
    </div>

    <!-- Results -->
    <div class="flex-1 overflow-y-auto">
      <div v-if="listResult === null" class="text-center text-neutral-500 py-8 text-sm">
        Enter search criteria above
      </div>
      <div v-else-if="!listResult.calls.length" class="text-center text-neutral-500 py-8 text-sm">
        No results found
      </div>
      <div v-else>
        <div class="px-3 py-1.5 text-xs text-neutral-500 border-b border-neutral-800">
          {{ listResult.count }} result{{ listResult.count !== 1 ? 's' : '' }}
        </div>
        <button
          v-for="call in listResult.calls"
          :key="call.id"
          class="w-full text-left px-3 py-2 border-b border-neutral-800 hover:bg-neutral-900 transition-colors"
          @click="emit('playCall', call.id)"
        >
          <div class="flex items-center justify-between">
            <span class="text-sm font-medium truncate">{{ call.talkgroupName }}</span>
            <span v-if="call.duration" class="text-xs text-neutral-500 ml-2 shrink-0">
              {{ Math.round(call.duration) }}s
            </span>
          </div>
          <div class="text-xs text-neutral-500 mt-0.5">{{ formatDateTime(call.dateTime) }}</div>
        </button>
      </div>
    </div>
  </div>
</template>
