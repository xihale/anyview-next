<script setup lang="ts">
// 递归变量节点: variate.innerObj 逐层展开(结构体/指针/数组)
import type { DbgVarNode } from "~/composables/useApi";
import { ChevronRight } from "lucide-vue-next";

const MAX_DEPTH = 24;
const CHILD_BATCH = 100;

const props = defineProps<{
  node: DbgVarNode;
  depth: number;
}>();

const open = ref(props.depth === 0);
const visibleCount = ref(CHILD_BATCH);

watch(
  () => props.node,
  () => {
    if (props.depth === 0) open.value = true;
    visibleCount.value = CHILD_BATCH;
  },
);

const kids = computed(() => props.node.innerObj ?? []);
const hasKids = computed(() => kids.value.length > 0);
const canExpand = computed(() => hasKids.value && props.depth < MAX_DEPTH);
const visibleKids = computed(() => kids.value.slice(0, visibleCount.value));
const remainingCount = computed(() => Math.max(0, kids.value.length - visibleCount.value));

function toggleOpen() {
  if (canExpand.value) open.value = !open.value;
}

function showMoreKids() {
  visibleCount.value += CHILD_BATCH;
}

const valClass = computed(() => {
  const v = props.node.value ?? "";
  if (/^0x/i.test(v) || v === "NULL" || v === "nil" || v === "(nil)") return "val-addr";
  if (/^-?\d+(?:\.\d+)?$/.test(v)) return "val-num";
  if (/^["'].*["']$/.test(v)) return "val-str";
  if (hasKids.value && !v) return "val-compound";
  return "val-text";
});

const displayVal = computed(() => {
  const v = props.node.value ?? "";
  if (v) return v.length > 60 ? `${v.slice(0, 60)}…` : v;
  if (hasKids.value) return "{…}";
  return "";
});
</script>

<template>
  <div class="var-node">
    <div
      class="var-row"
      :class="{ expandable: canExpand }"
      :role="canExpand ? 'button' : undefined"
      :tabindex="canExpand ? 0 : undefined"
      :aria-expanded="canExpand ? open : undefined"
      @click="toggleOpen"
      @keydown.enter.prevent="toggleOpen"
      @keydown.space.prevent="toggleOpen"
    >
      <ChevronRight
        v-if="canExpand"
        class="var-caret icon"
        :class="{ open }"
        :size="11"
        :stroke-width="2"
      />
      <span v-else class="var-spacer" />

      <span class="var-name">{{ node.name }}</span>

      <span v-if="node.type" class="var-type" :title="node.type">{{ node.type }}</span>

      <span class="var-eq">=</span>

      <span
        class="var-val"
        :class="valClass"
        :title="node.value ?? ''"
      >
        {{ displayVal }}
      </span>
    </div>

    <ul v-if="canExpand && open" class="var-kids">
      <li v-for="(k, idx) in visibleKids" :key="`${idx}:${k.name}`">
        <DebugVarNode :node="k" :depth="props.depth + 1" />
      </li>
      <li v-if="remainingCount > 0">
        <button type="button" class="btn btn-ghost var-more-btn" @click="showMoreKids">
          显示更多（剩余 {{ remainingCount }} 项）
        </button>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.var-node {
  min-width: 0;
}

.var-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 3px 6px;
  border-radius: 3px;
  cursor: default;
  white-space: nowrap;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  font-size: 12px;
  line-height: 1.4;
  transition: background-color 0.12s ease;
}

.var-row:hover {
  background: var(--bg-elevated);
}

.var-row.expandable {
  cursor: pointer;
}

.var-caret {
  flex: none;
  color: var(--text-muted);
  transition: transform 0.12s ease;
}

.var-caret.open {
  transform: rotate(90deg);
}

.var-spacer {
  flex: none;
  width: 11px;
}

.var-name {
  color: var(--text-primary);
  font-weight: 600;
  flex: none;
}

.var-type {
  color: var(--text-muted);
  font-size: 10.5px;
  background: var(--bg-canvas);
  padding: 0 4px;
  border-radius: 2px;
  border: 1px solid var(--border-subtle);
  flex: none;
}

.var-eq {
  color: var(--text-muted);
  font-size: 11px;
  flex: none;
}

.var-val {
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
}

.var-val.val-num {
  color: #ffa657;
}

.var-val.val-addr {
  color: #79c0ff;
}

.var-val.val-str {
  color: #7ee787;
}

.var-val.val-compound {
  color: var(--text-muted);
  font-style: italic;
}

:global(html[data-theme="light"] .var-val.val-num) {
  color: #953800;
}

:global(html[data-theme="light"] .var-val.val-addr) {
  color: #0550ae;
}

:global(html[data-theme="light"] .var-val.val-str) {
  color: #116329;
}

.var-kids {
  list-style: none;
  margin: 0;
  padding: 0 0 0 16px;
  border-left: 1px solid var(--border-subtle);
  margin-left: 5px;
}

.var-more-btn {
  font-size: 11px;
  font-family: var(--font-mono);
  padding: 2px 6px;
  margin-top: 2px;
  color: var(--text-muted);
}

@media (prefers-reduced-motion: reduce) {
  .var-caret {
    transition: none;
  }
}
</style>
