<script setup lang="ts">
import type { RdioConfig, RdioLivefeedMap } from '~/composables/useRdioScanner'

const props = defineProps<{
  config: RdioConfig | null
  livefeedMap: RdioLivefeedMap
}>()

const emit = defineEmits<{
  toggleSystem: [systemRef: number, enabled: boolean]
  toggleTalkgroup: [systemRef: number, talkgroupRef: number, enabled: boolean]
  close: []
}>()

const expandedSystems = ref<Record<number, boolean>>({})

const systemStatus = (systemRef: number) => {
  const sys = props.config?.systems.find(s => s.id === systemRef)
  if (!sys) return 'off'
  const lfm = props.livefeedMap[systemRef]
  if (!lfm) return 'off'
  const active = sys.talkgroups.filter(tg => lfm[tg.id]).length
  if (active === 0) return 'off'
  if (active === sys.talkgroups.length) return 'on'
  return 'partial'
}

const groupedTalkgroups = (sys: RdioConfig['systems'][0]) => {
  const groups: Record<string, typeof sys.talkgroups> = {}
  sys.talkgroups.forEach(tg => {
    const g = tg.group || 'Other'
    if (!groups[g]) groups[g] = []
    groups[g].push(tg)
  })
  return groups
}
</script>

<template>
  <div class="flex flex-col h-full">
    <div class="flex items-center justify-between px-4 py-3 border-b border-neutral-800">
      <span class="font-semibold text-sm">Live Feed Selection</span>
      <UButton icon="i-heroicons-x-mark" variant="ghost" size="xs" @click="emit('close')" />
    </div>

    <div class="flex-1 overflow-y-auto p-3 space-y-2">
      <div v-if="!config?.systems.length" class="text-center text-neutral-500 py-8 text-sm">
        No systems configured
      </div>

      <div v-for="sys in config?.systems" :key="sys.id" class="rounded-lg border border-neutral-800">
        <!-- System header -->
        <div
          class="flex items-center gap-2 p-3 cursor-pointer hover:bg-neutral-900 rounded-t-lg"
          @click="expandedSystems[sys.id] = !expandedSystems[sys.id]"
        >
          <UCheckbox
            :model-value="systemStatus(sys.id) === 'on'"
            :indeterminate="systemStatus(sys.id) === 'partial'"
            @update:model-value="emit('toggleSystem', sys.id, $event)"
            @click.stop
          />
          <span class="font-medium text-sm flex-1">{{ sys.label }}</span>
          <UBadge variant="soft" size="xs">{{ sys.talkgroups.length }}</UBadge>
          <UIcon
            :name="expandedSystems[sys.id] ? 'i-heroicons-chevron-up' : 'i-heroicons-chevron-down'"
            class="text-neutral-400 size-4"
          />
        </div>

        <!-- Talkgroups -->
        <Transition name="fade">
          <div v-if="expandedSystems[sys.id]" class="border-t border-neutral-800 p-2 space-y-3">
            <div v-for="(tgs, group) in groupedTalkgroups(sys)" :key="group">
              <div class="text-xs text-neutral-500 uppercase tracking-wider px-2 py-1">{{ group }}</div>
              <div
                v-for="tg in tgs"
                :key="tg.id"
                class="flex items-center gap-2 px-2 py-1.5 rounded hover:bg-neutral-900"
              >
                <UCheckbox
                  :model-value="!!livefeedMap[sys.id]?.[tg.id]"
                  @update:model-value="emit('toggleTalkgroup', sys.id, tg.id, $event)"
                />
                <span class="text-sm flex-1 truncate">{{ tg.name || tg.label }}</span>
                <span class="text-xs text-neutral-500 font-mono">{{ tg.id }}</span>
              </div>
            </div>
          </div>
        </Transition>
      </div>
    </div>
  </div>
</template>

<style scoped>
.fade-enter-active, .fade-leave-active { transition: opacity 0.15s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
