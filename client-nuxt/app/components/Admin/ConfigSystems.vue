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

const expandedSystem = ref<number | null>(null)
const expandedTG = ref<number | null>(null)

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
}

const removeSystem = (i: number) => {
  systems.value = systems.value.filter((_, idx) => idx !== i)
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
  { label: 'P25', value: 'p25' },
  { label: 'P25 Phase 2', value: 'p25p2' },
  { label: 'NFM (analog)', value: 'nfm' },
  { label: 'DMR', value: 'dmr' },
  { label: 'NXDN', value: 'nxdn' },
]
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

    <UAccordion
      v-for="(sys, si) in systems"
      :key="si"
      :items="[{ label: sys.label || 'New System', slot: 'system-' + si }]"
      class="border border-neutral-800 rounded-lg"
    >
      <template #[`system-${si}`]>
        <div class="p-4 space-y-4">
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
              <div
                v-for="(tg, ti) in (sys.talkgroups ?? [])"
                :key="ti"
                class="grid grid-cols-12 gap-2 px-3 py-1.5 border-t border-neutral-800 items-center text-sm"
              >
                <UInput v-model.number="tg.talkgroupRef" type="number" size="xs" class="col-span-1" />
                <UInput v-model="tg.label" size="xs" class="col-span-2" maxlength="8" />
                <UInput v-model="tg.name" size="xs" class="col-span-3" />
                <USelect v-model="tg.type" :items="tgTypeOptions" size="xs" class="col-span-2" />
                <USelect v-model="tg.groupIds[0]" :items="groupOptions" size="xs" class="col-span-2" />
                <USelect v-model="tg.tagId" :items="tagOptions" size="xs" class="col-span-1" />
                <UButton
                  icon="i-heroicons-trash"
                  color="error"
                  variant="ghost"
                  size="xs"
                  class="col-span-1"
                  @click="sys.talkgroups?.splice(ti, 1)"
                />
              </div>
              <div v-if="!(sys.talkgroups ?? []).length" class="px-3 py-4 text-xs text-neutral-600 text-center">
                No talkgroups — import from CSV or add manually.
              </div>
            </div>
          </div>

          <div class="flex justify-end pt-2">
            <UButton icon="i-heroicons-trash" color="error" variant="ghost" size="sm" @click="removeSystem(si)">
              Remove system
            </UButton>
          </div>
        </div>
      </template>
    </UAccordion>
  </div>
</template>
