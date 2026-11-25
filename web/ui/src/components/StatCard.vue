<script setup>
/* 指标卡：默认纯展示；传 clickable 后整卡可点（右侧显示小箭头），点按 emit('click')。 */
const props = defineProps({
  label: { type: String, required: true },
  value: { type: String, required: true },
  sub: { type: String, default: '' },
  clickable: { type: Boolean, default: false },
})
const emit = defineEmits(['click'])
function onClick() { if (props.clickable) emit('click') }
</script>

<template>
  <component :is="clickable ? 'button' : 'div'" class="metric" :class="{ clickable }"
             :type="clickable ? 'button' : undefined" @click="onClick">
    <span class="metric-top">
      <span class="label">{{ label }}</span>
      <svg v-if="clickable" class="chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 5l7 7-7 7"/></svg>
    </span>
    <span class="value">{{ value }}</span>
    <span v-if="sub" class="sub">{{ sub }}</span>
  </component>
</template>

<style scoped>
.metric {
  background: var(--card); border-radius: var(--r);
  box-shadow: var(--shadow); padding: 12px 14px;
  display: flex; flex-direction: column; gap: 3px;
  width: 100%; text-align: left; color: inherit; font: inherit;
}
.metric.clickable { cursor: pointer; transition: opacity 0.15s; }
.metric.clickable:active { opacity: 0.65; }
.metric-top { display: flex; align-items: center; justify-content: space-between; }
.chev { width: 13px; height: 13px; color: var(--fg3); flex: none; }
.label { font-size: 12px; color: var(--fg2); }
.value { font-size: 20px; font-weight: 700; letter-spacing: -0.2px; }
.sub { font-size: 11px; color: var(--fg3); }
</style>
