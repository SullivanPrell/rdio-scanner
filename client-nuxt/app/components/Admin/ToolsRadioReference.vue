<script setup lang="ts">
import type { AdminSystem } from '~/composables/useAdmin'

const props = defineProps<{ systems: AdminSystem[] }>()
const admin = useAdmin()
const toast = useToast()

const username = ref('')
const password = ref('')
const states = ref<Array<{ id: number; name: string }>>([])
const counties = ref<Array<{ id: number; name: string }>>([])
const selectedState = ref<number | null>(null)
const selectedCounty = ref<number | null>(null)
const systemRef = ref(1)
const portBase = ref(50000)
const loading = ref(false)

const fetchStates = async () => {
  if (!username.value || !password.value) return
  loading.value = true
  const result = await admin.getRRStates(username.value, password.value)
  states.value = (result as typeof states.value) ?? []
  loading.value = false
}

const fetchCounties = async () => {
  if (!selectedState.value) return
  loading.value = true
  const result = await admin.getRRCounties(username.value, password.value, selectedState.value)
  counties.value = (result as typeof counties.value) ?? []
  loading.value = false
}

const doImport = async () => {
  if (!selectedCounty.value) return
  loading.value = true
  const result = await admin.importRRCounty(username.value, password.value, selectedCounty.value, systemRef.value, portBase.value)
  loading.value = false
  if (result) toast.add({ title: 'RadioReference import complete', color: 'success' })
}

watch(selectedState, () => {
  counties.value = []
  selectedCounty.value = null
  if (selectedState.value) fetchCounties()
})
</script>

<template>
  <UCard>
    <template #header><span class="font-semibold">RadioReference API Import</span></template>

    <div class="space-y-4">
      <UAlert color="info" icon="i-heroicons-information-circle" description="Requires a RadioReference Premium subscription." />

      <div class="grid grid-cols-2 gap-3">
        <UFormField label="Username">
          <UInput v-model="username" autocomplete="off" />
        </UFormField>
        <UFormField label="Password">
          <UInput v-model="password" type="password" autocomplete="off" />
        </UFormField>
      </div>

      <UButton :loading="loading" icon="i-heroicons-arrow-path" @click="fetchStates">
        Load States
      </UButton>

      <template v-if="states.length">
        <UFormField label="State">
          <USelect
            v-model.number="selectedState"
            :options="states.map(s => ({ label: s.name, value: s.id }))"
            placeholder="Select state"
          />
        </UFormField>
      </template>

      <template v-if="counties.length">
        <UFormField label="County">
          <USelect
            v-model.number="selectedCounty"
            :options="counties.map(c => ({ label: c.name, value: c.id }))"
            placeholder="Select county"
          />
        </UFormField>

        <div class="grid grid-cols-2 gap-3">
          <UFormField label="System ref">
            <UInput v-model.number="systemRef" type="number" min="1" />
          </UFormField>
          <UFormField label="Bridge port base">
            <UInput v-model.number="portBase" type="number" min="1024" />
          </UFormField>
        </div>

        <UButton
          icon="i-heroicons-arrow-down-tray"
          :loading="loading"
          :disabled="!selectedCounty"
          @click="doImport"
        >
          Import County
        </UButton>
      </template>
    </div>
  </UCard>
</template>
