<script setup lang="ts">
import type { RdioCall, RdioConfig } from '~/composables/useRdioScanner'

const props = defineProps<{
  call: RdioCall | null
  config: RdioConfig | null
  history: RdioCall[]
  isPlaying: boolean
  isPaused: boolean
  progress: number
  listenersCount: number
  showListenersCount: boolean
  time12h: boolean
  connected: boolean
  clock: string
  freq: number
  error: number
  spike: number
  unit: number
  ledColor: string | null
  queueSize: number
}>()

const fmtTime = (iso: string | undefined, opts?: Intl.DateTimeFormatOptions) => {
  if (!iso) return '--:--:--'
  const d = new Date(iso)
  return d.toLocaleTimeString('en-US', { hour12: props.time12h, ...opts })
}

const fmtDate = (iso: string | undefined) => {
  if (!iso) return '--/--'
  const d = new Date(iso)
  return `${String(d.getMonth() + 1).padStart(2, '0')}/${String(d.getDate()).padStart(2, '0')}`
}

const fmtFreq = (hz: number) => {
  if (!hz) return '0'
  return (hz / 1e6).toFixed(4)
}

const active = computed(() => props.isPlaying || props.isPaused)
const ledStyle = computed(() => {
  if (!props.isPlaying && !props.isPaused) return {}
  const color = props.ledColor || 'rgb(0, 230, 118)'
  return { background: color, boxShadow: `0 0 6px 3px ${color}` }
})
const ledClass = computed(() => ({
  'led-on': props.isPlaying,
  'led-paused': props.isPaused,
}))
</script>

<template>
  <div class="rdio-display" :class="{ idle: !active }">
    <!-- Row 1: time | link/listeners | queue -->
    <div class="row three">
      <div><span>{{ fmtTime(clock) }}</span></div>
      <div>
        <span v-if="!connected">NO LINK</span>
        <span v-else-if="showListenersCount">L: {{ listenersCount }}</span>
      </div>
      <div><span>Q: {{ queueSize }}</span></div>
    </div>

    <!-- Row 2: small spacer -->
    <div class="row small"></div>

    <!-- Row 3: system | type | tag -->
    <div class="row three">
      <div><span>{{ call?.systemLabel || '' }}</span></div>
      <div><span>{{ call?.talkgroupType?.toUpperCase() || '' }}</span></div>
      <div><span>{{ call?.talkgroupTag || '' }}</span></div>
    </div>

    <!-- Row 4: talkgroup label | | date + progress time -->
    <div class="row three">
      <div><span>{{ call?.talkgroupLabel || '' }}</span></div>
      <div></div>
      <div>
        <span>{{ fmtDate(call?.dateTime) }}&nbsp;</span>
        <span>{{ fmtTime(call?.dateTime) }}</span>
      </div>
    </div>

    <!-- Row 5: BIG talkgroup name -->
    <div class="row big full">
      <span>{{ call?.talkgroupName || (active ? '…' : 'Scanning') }}</span>
    </div>

    <!-- Row 6: frequency | | TGID -->
    <div class="row three">
      <div><span>F: {{ fmtFreq(freq) }}</span></div>
      <div></div>
      <div><span>TGID: {{ call?.talkgroup || 0 }}</span></div>
    </div>

    <!-- Row 7: error/spike | | unit -->
    <div class="row three">
      <div><span>E: {{ error }} S: {{ spike }}</span></div>
      <div></div>
      <div><span v-if="unit">UID: {{ unit }}</span></div>
    </div>

    <!-- Row 8: progress bar -->
    <div class="progress-row">
      <div class="progress-bar" :style="{ width: `${isPlaying ? progress : 0}%` }" />
    </div>

    <!-- Call history table -->
    <div class="history-wrapper">
      <table class="history">
        <thead>
          <tr>
            <th>Time</th>
            <th>System</th>
            <th>Talkgroup</th>
            <th>Name</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="h in history"
            :key="h.id"
            :class="{ replaying: h.id === call?.id }"
          >
            <td>{{ fmtTime(h.dateTime, { hour: '2-digit', minute: '2-digit' }) }}</td>
            <td>{{ h.systemLabel }}</td>
            <td>{{ h.talkgroupLabel }}</td>
            <td>{{ h.talkgroupName }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>

  <!-- Status bar (above display) — LED + branding -->
  <div class="rdio-status">
    <span class="branding">{{ config?.branding || 'Rdio Scanner' }}</span>
    <span class="led" :class="ledClass" :style="ledStyle" />
  </div>
</template>

<style scoped>
.rdio-status {
  display: flex;
  align-items: center;
  flex-direction: row;
  min-height: 1.5rem;
  margin-bottom: 16px;
  order: -1;
}
.branding {
  flex: 1;
  color: rgb(64, 64, 64);
  font-size: 16px;
  font-weight: normal;
  letter-spacing: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  text-shadow: -1px -1px 1px rgba(0,0,0,.7), 1px 1px 1px rgba(255,255,255,.3);
  text-transform: uppercase;
  white-space: nowrap;
}
.led {
  display: block;
  width: 24px;
  height: 12px;
  background: rgb(80, 80, 80);
  margin-left: 24px;
}
.led.led-on {
  background: rgb(0, 230, 118);
  box-shadow: 0 0 6px 3px rgb(0, 230, 118);
}
.led.led-paused {
  background: rgb(0, 230, 118);
  box-shadow: 0 0 6px 3px rgb(0, 230, 118);
  animation: led-blink 2s step-end infinite;
}
@keyframes led-blink {
  50% { background: rgb(80,80,80); box-shadow: none; }
}

.rdio-display {
  background: rgb(209, 238, 238);
  box-shadow: 2px 2px 4px rgb(0,0,0) inset, 1px 1px 2px 1px rgb(255,255,255);
  color: rgba(0,0,0,.8);
  cursor: default;
  display: block;
  font-size: 14px;
  font-weight: 400;
  line-height: 20px;
  padding: 8px;
  margin-bottom: 24px;
  font-family: 'Roboto Mono', 'Courier New', monospace;
}
.rdio-display.idle {
  background: rgb(190, 190, 174);
}
.rdio-display * {
  overflow: hidden;
  text-overflow: clip;
  white-space: nowrap;
}

.row {
  display: flex;
  flex-direction: row;
  height: 20px;
}
.row.three > * {
  display: flex;
  flex: 33%;
  flex-direction: row;
}
.row.three > *:nth-child(2) { justify-content: center; }
.row.three > *:nth-child(3) { justify-content: flex-end; }
.row.big {
  font-size: 24px;
  height: 32px;
  line-height: 32px;
  font-weight: 700;
}
.row.small {
  font-size: 12px;
  height: 14px;
  line-height: 14px;
}
.row.full { display: block; }

.progress-row {
  height: 4px;
  background: rgba(0,0,0,.15);
  margin: 4px 0;
  border-radius: 2px;
  overflow: hidden;
}
.progress-bar {
  height: 100%;
  background: rgba(0,0,0,.4);
  transition: width .1s linear;
}

.history-wrapper { position: relative; }
.history {
  border-collapse: collapse;
  font-size: 11px;
  table-layout: fixed;
  width: 100%;
}
.history td, .history th {
  padding: 0 6px;
  text-align: start;
}
.history td:nth-child(1), .history th:nth-child(1) { width: 12%; }
.history td:nth-child(2), .history th:nth-child(2) { width: 23%; }
.history td:nth-child(3), .history th:nth-child(3) { width: 25%; }
.history td:nth-child(4), .history th:nth-child(4) { width: 40%; }
.history th { color: rgba(0,0,0,.4); font-weight: 400; text-transform: uppercase; }
.history tbody > tr { border-top: 1px solid rgba(0,0,0,.2); height: 21px; }
.history tr.replaying { font-weight: 700; }
</style>
