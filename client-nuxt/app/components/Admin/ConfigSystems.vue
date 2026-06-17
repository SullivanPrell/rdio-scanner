<script setup lang="ts">
import type { AdminSystem, Group, Tag } from '~/composables/useAdmin'

const props = defineProps<{
  modelValue: AdminSystem[]
  groups: Group[]
  tags: Tag[]
}>()
const emit = defineEmits<{ 'update:modelValue': [AdminSystem[]] }>()

const systems = computed<AdminSystem[]>({
  get: () => props.modelValue ?? [],
  set: v => emit('update:modelValue', v),
})

// Only one system's talkgroups are rendered at a time. Collapsed systems are
// just a header row, so a config with hundreds of systems (or one system with
// hundreds of talkgroups) stays responsive — the old UAccordion mounted a Reka
// USelect per talkgroup field, which froze the tab past ~50 rows.
const expanded = ref<number | null>(null)
const toggle = (i: number) => { expanded.value = expanded.value === i ? null : i }

const addSystem = () => {
  const maxRef = systems.value.reduce((m, s) => Math.max(m, s.systemRef), 0)
  systems.value = [...systems.value, {
    label: 'New System',
    systemRef: maxRef + 1,
    type: '',
    alert: '',
    autoPopulate: false,
    blacklists: '',
    delay: 0,
    led: '',
    talkgroups: [],
    sites: [],
    units: [],
  }]
  expanded.value = systems.value.length - 1
}

const removeSystem = (i: number) => {
  systems.value = systems.value.filter((_, idx) => idx !== i)
  // Keep the expanded pointer aligned with the shifted indices.
  if (expanded.value === i) expanded.value = null
  else if (expanded.value !== null && expanded.value > i) expanded.value--
}

const addTalkgroup = (sys: AdminSystem) => {
  if (!sys.talkgroups) sys.talkgroups = []
  const maxRef = sys.talkgroups.reduce((m, t) => Math.max(m, t.talkgroupRef), 0)
  sys.talkgroups.push({
    talkgroupRef: maxRef + 1,
    label: '',
    name: '',
    type: '',
    alert: '',
    delay: 0,
    led: '',
    groupIds: [],
  })
}

const groupOptions = computed(() => (props.groups ?? []).map(g => ({ label: g.label, value: g.id! })))
const tagOptions = computed(() => (props.tags ?? []).map(t => ({ label: t.label, value: t.id! })))

const tgTypeOptions = [
  { label: '—', value: '' },
  { label: 'P25', value: 'p25' },
  { label: 'P25 Phase 2', value: 'p25p2' },
  { label: 'NFM (analog)', value: 'nfm' },
  { label: 'DMR', value: 'dmr' },
  { label: 'NXDN', value: 'nxdn' },
]

// Shared style for the lightweight native inputs/selects in the talkgroup grid.
const cell = 'w-full bg-neutral-900 border border-neutral-800 rounded px-1.5 py-1 text-xs text-white focus:outline-none focus:border-neutral-600'
</script>

<template>
  <div class="space-y-3">
    <div class="flex justify-end">
      <UButton icon="i-heroicons-plus" size="sm" variant="soft" @click="addSystem">
        Add System
      </UButton>
    </div>

    <div v-if="!systems.length" class="text-center text-neutral-500 py-8 text-sm">
      No systems configured. Add one to get started.
    </div>

    <div
      v-for="(sys, si) in systems"
      :key="si"
      class="border border-neutral-800 rounded-lg overflow-hidden"
    >
      <!-- System header — always rendered, cheap. Delete lives here so a whole
           system can be removed without expanding it. -->
      <div
        class="flex items-center gap-2 px-3 py-2.5 cursor-pointer hover:bg-neutral-900 transition-colors"
        @click="toggle(si)"
      >
        <UIcon
          :name="expanded === si ? 'i-heroicons-chevron-down' : 'i-heroicons-chevron-right'"
          class="size-4 text-neutral-400 flex-shrink-0"
        />
        <span class="font-medium text-sm flex-1 truncate">{{ sys.label || 'New System' }}</span>
        <span class="text-xs text-neutral-500 font-mono">ref {{ sys.systemRef }}</span>
        <UBadge variant="soft" size="xs" color="neutral">{{ (sys.talkgroups ?? []).length }} TG</UBadge>
        <UButton
          icon="i-heroicons-trash"
          color="error"
          variant="ghost"
          size="xs"
          aria-label="Delete system"
          @click.stop="removeSystem(si)"
        />
      </div>

      <!-- System body — mounted only while expanded. -->
      <div v-if="expanded === si" class="p-4 space-y-4 border-t border-neutral-800">
        <!-- System fields -->
        <div class="grid grid-cols-2 gap-3">
          <UFormField label="Label">
            <UInput v-model="sys.label" />
          </UFormField>
          <UFormField label="System Ref">
            <UInput v-model.number="sys.systemRef" type="number" min="1" />
          </UFormField>
          <UFormField label="Type">
            <UInput v-model="sys.type" placeholder="p25, dmr, nfm…" />
          </UFormField>
          <UFormField label="LED color">
            <UInput v-model="sys.led" type="color" />
          </UFormField>
          <UFormField label="Delay (s)">
            <UInput v-model.number="sys.delay" type="number" min="0" />
          </UFormField>
          <UFormField label="Blacklists">
            <UInput v-model="sys.blacklists" placeholder="Comma-separated IDs" />
          </UFormField>
        </div>

        <div class="flex items-center gap-4">
          <UCheckbox v-model="sys.autoPopulate" label="Auto-populate talkgroups" />
        </div>

        <!-- Talkgroups -->
        <div>
          <div class="flex items-center justify-between mb-2">
            <span class="text-sm font-semibold text-neutral-300">Talkgroups ({{ (sys.talkgroups ?? []).length }})</span>
            <UButton icon="i-heroicons-plus" size="xs" variant="ghost" @click="addTalkgroup(sys)">
              Add
            </UButton>
          </div>

          <div class="rounded border border-neutral-800 overflow-hidden">
            <!-- Header row -->
            <div class="grid grid-cols-12 gap-2 px-3 py-1.5 bg-neutral-900 text-xs text-neutral-500 font-medium">
              <span class="col-span-1">Ref</span>
              <span class="col-span-2">Label</span>
              <span class="col-span-3">Name</span>
              <span class="col-span-2">Type</span>
              <span class="col-span-2">Group</span>
              <span class="col-span-1">Tag</span>
              <span class="col-span-1" />
            </div>
            <!-- Rows use native inputs/selects (not Nuxt UI components) so a few
                 hundred talkgroups render instantly instead of mounting thousands
                 of Reka widgets. -->
            <div
              v-for="(tg, ti) in (sys.talkgroups ?? [])"
              :key="ti"
              class="grid grid-cols-12 gap-2 px-3 py-1.5 border-t border-neutral-800 items-center"
            >
              <input v-model.number="tg.talkgroupRef" type="number" :class="['col-span-1', cell]">
              <input v-model="tg.label" maxlength="8" :class="['col-span-2', cell]">
              <input v-model="tg.name" :class="['col-span-3', cell]">
              <select v-model="tg.type" :class="['col-span-2', cell]">
                <option v-for="o in tgTypeOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
              </select>
              <select v-model.number="tg.groupIds[0]" :class="['col-span-2', cell]">
                <option :value="undefined">—</option>
                <option v-for="o in groupOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
              </select>
              <select v-model.number="tg.tagId" :class="['col-span-1', cell]">
                <option :value="undefined">—</option>
                <option v-for="o in tagOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
              </select>
              <button
                type="button"
                class="col-span-1 text-red-500 hover:text-red-400 text-sm leading-none"
                aria-label="Delete talkgroup"
                @click="sys.talkgroups?.splice(ti, 1)"
              >
                ✕
              </button>
            </div>
            <div v-if="!(sys.talkgroups ?? []).length" class="px-3 py-4 text-xs text-neutral-600 text-center">
              No talkgroups — import from CSV or add manually.
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
