<script setup lang="ts">
const props = defineProps<{
  livefeedOnline: boolean
  livefeedOffline: boolean
  livefeedPaused: boolean
  playbackMode: boolean
  holdSys: boolean
  holdTg: boolean
  isPlaying: boolean
  isPaused: boolean
}>()

const emit = defineEmits<{
  livefeed: []
  holdSystem: []
  holdTalkgroup: []
  replay: []
  skip: []
  avoid: []
  search: []
  pause: []
  select: []
}>()

const livefeedClass = computed(() => {
  if (props.playbackMode) return 'partial'
  if (props.livefeedOnline) return 'on'
  return 'off'
})
</script>

<template>
  <div class="rdio-control">
    <div class="row">
      <button class="rdio-button" :class="livefeedClass" @click="emit('livefeed')">
        LIVE<br>FEED
      </button>
      <div class="spacer" />
      <button class="rdio-button" :class="holdSys ? 'on' : 'off'" @click="emit('holdSystem')">
        HOLD<br>SYS
      </button>
      <div class="spacer" />
      <button class="rdio-button" :class="holdTg ? 'on' : 'off'" @click="emit('holdTalkgroup')">
        HOLD<br>TG
      </button>
    </div>
    <div class="row">
      <button class="rdio-button" @click="emit('replay')">
        REPLAY<br>LAST
      </button>
      <div class="spacer" />
      <button class="rdio-button" @click="emit('skip')">
        SKIP<br>NEXT
      </button>
      <div class="spacer" />
      <button class="rdio-button" @click="emit('avoid')">
        AVOID
      </button>
    </div>
    <div class="row">
      <button class="rdio-button" @click="emit('search')">
        SEARCH<br>CALL
      </button>
      <div class="spacer" />
      <button class="rdio-button" :class="livefeedPaused ? 'on' : 'off'" @click="emit('pause')">
        PAUSE
      </button>
      <div class="spacer" />
      <button class="rdio-button" @click="emit('select')">
        SELECT<br>TG
      </button>
    </div>
  </div>
</template>

<style scoped>
.rdio-control .row {
  display: flex;
  flex-direction: row;
  justify-content: space-between;
  margin-bottom: 12px;
}
.spacer {
  display: block;
  width: 24px;
  flex-shrink: 0;
}
.rdio-button {
  --def: rgb(45, 45, 45);
  --green: rgb(0, 230, 118);
  --red: rgb(255, 23, 68);
  --yellow: rgb(255, 234, 0);

  background: var(--def);
  border-style: solid;
  border-width: 1px;
  border-bottom-color: rgba(0,0,0,.87);
  border-left-color:   rgba(255,255,255,.7);
  border-right-color:  rgba(0,0,0,.87);
  border-top-color:    rgba(255,255,255,.7);
  color: rgb(250, 250, 250);
  cursor: pointer;
  flex: 1;
  font-family: inherit;
  font-size: 12px;
  font-weight: 500;
  height: 40px;
  line-height: 18px;
  margin: 2px;
  min-width: 80px;
  overflow: hidden;
  padding: 2px 8px;
  position: relative;
  text-overflow: clip;
  text-shadow: 0 0 4px rgb(0,0,0);
  text-transform: uppercase;
  white-space: normal;
  box-sizing: content-box;
}
.rdio-button:focus { outline: 0; }
.rdio-button:active {
  top: 2px;
  transform: scale(0.98);
  transform-origin: bottom center;
}

/* Status dot */
.rdio-button.off::after,
.rdio-button.on::after,
.rdio-button.partial::after {
  content: '';
  display: block;
  height: 6px;
  position: absolute;
  right: 4px;
  top: 4px;
  width: 6px;
  border-radius: 50%;
}
.rdio-button.off::after {
  background: var(--red);
  box-shadow: 1px 1px 1px rgba(255,255,255,.7) inset, 0 0 3px 1px var(--red);
}
.rdio-button.on::after {
  background: var(--green);
  box-shadow: 1px 1px 1px rgba(255,255,255,.7) inset, 0 0 3px 1px var(--green);
}
.rdio-button.partial::after {
  background: var(--yellow);
  box-shadow: 1px 1px 1px rgba(255,255,255,.7) inset, 0 0 3px 1px var(--yellow);
}
</style>
