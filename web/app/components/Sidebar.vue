<script setup lang="ts">
import type { CatalogItem } from "~/composables/useApi";
import { Search, X, ChevronRight, Sun, Moon, Settings, LogOut } from "lucide-vue-next";

const props = defineProps<{
  groups: { chap: string; items: CatalogItem[] }[];
  activeEid: number;
  passed: number;
  total: number;
  open: boolean;
  studentName?: string;
  mock?: boolean;
  kicked?: boolean;
  offline?: boolean;
  wafCooldown?: string;
  loading?: boolean;
  error?: string;
  isDark?: boolean;
}>();

const emit = defineEmits<{
  (e: "select", eid: number): void;
  (e: "open-course-modal"): void;
  (e: "open-settings"): void;
  (e: "toggle-theme"): void;
  (e: "logout"): void;
  (e: "close"): void;
  (e: "retry"): void;
}>();

const sid = defineModel<number>("sid", { required: true });
const cid = defineModel<number>("cid", { required: true });

const searchQuery = ref("");
const filterType = ref<"all" | "todo" | "done">("all");
// 章节折叠: undefined=没动过(走默认: 只展开当前题所在章), true/false=用户手动切过(优先)
const collapsedChapters = ref<Record<string, boolean | undefined>>({});

// 搜索/筛选时无视折叠全展开——命中被藏住等于没搜到
function chapterExpanded(chap: string, items: CatalogItem[]): boolean {
  const manual = collapsedChapters.value[chap];
  if (manual !== undefined) return !manual;
  if (searchQuery.value.trim() || filterType.value !== "all") return true;
  return items.some((i) => i.eid === props.activeEid);
}

function toggleChapter(chap: string, items: CatalogItem[]) {
  collapsedChapters.value[chap] = chapterExpanded(chap, items);
}

watch(sid, () => {
  collapsedChapters.value = {};
});

const filteredGroups = computed(() => {
  const q = searchQuery.value.trim().toLowerCase();
  return props.groups
    .map((g) => {
      const items = g.items.filter((item) => {
        if (filterType.value === "todo" && item.pass) return false;
        if (filterType.value === "done" && !item.pass) return false;

        if (!q) return true;
        return (
          item.pname.toLowerCase().includes(q) ||
          (item.pmemo && item.pmemo.toLowerCase().includes(q))
        );
      });
      return {
        chap: g.chap,
        items,
        totalInChap: g.items.length,
        passedInChap: g.items.filter((i) => i.pass).length,
      };
    })
    .filter((g) => g.items.length > 0);
});

function resetFilters() {
  searchQuery.value = "";
  filterType.value = "all";
}

const searchInputRef = ref<HTMLInputElement | null>(null);
const listRef = ref<HTMLElement | null>(null);

function scrollActiveIntoView() {
  nextTick(() => {
    const activeEl = listRef.value?.querySelector<HTMLElement>(".problem-row.active");
    activeEl?.scrollIntoView({ block: "nearest" });
  });
}

watch(() => props.activeEid, (eid) => {
  if (eid) scrollActiveIntoView();
});

watch(() => props.open, (isOpen) => {
  if (isOpen) scrollActiveIntoView();
});

defineExpose({
  focusSearch: () => searchInputRef.value?.focus(),
});
</script>

<template>
  <aside class="sidebar" aria-label="题库侧栏" :class="{ 'mobile-open': open }">
    <!-- 状态条: 仅在 mock/被顶号/离线/WAF冷却 时出现 -->
    <div v-if="mock || kicked || offline || wafCooldown" class="status-strip" role="status">
      <span v-if="mock" class="status-item">
        <span class="status-dot-inline" />
        Mock
      </span>
      <span v-if="offline" class="status-item warn">
        <span class="status-dot-inline" />
        离线
      </span>
      <span v-if="wafCooldown" class="status-item warn" :title="wafCooldown">
        <span class="status-dot-inline" />
        WAF 冷却中
      </span>
      <span v-if="kicked" class="status-item err">
        <span class="status-dot-inline" />
        已被顶号
      </span>
    </div>

    <!-- 侧栏顶端检索与筛选 -->
    <div class="sidebar-top">
      <div class="search-row">
        <div class="search-wrap">
          <Search class="icon search-icon" :size="12" :stroke-width="1.5" />
          <input
            ref="searchInputRef"
            v-model="searchQuery"
            type="text"
            class="search-input"
            placeholder="搜索"
            title="按 / 聚焦"
            aria-label="搜索题目"
            @keydown.esc="searchQuery ? (searchQuery = '') : emit('close')"
          >
          <button v-if="searchQuery" type="button" class="clear-btn" aria-label="清空搜索" @click="searchQuery = ''">
            <X class="icon" :size="11" :stroke-width="1.5" />
          </button>
        </div>

        <button
          type="button"
          class="btn-icon mobile-close-btn"
          aria-label="关闭题库"
          @click="emit('close')"
        >
          <X class="icon" :size="14" :stroke-width="1.5" />
        </button>
      </div>

      <div class="filter-bar">
        <div class="filter-tabs" role="group" aria-label="题目筛选">
          <button
            type="button"
            class="tab-btn"
            :class="{ active: filterType === 'all' }"
            :aria-pressed="filterType === 'all'"
            @click="filterType = 'all'"
          >
            全部
          </button>
          <button
            type="button"
            class="tab-btn"
            :class="{ active: filterType === 'todo' }"
            :aria-pressed="filterType === 'todo'"
            @click="filterType = 'todo'"
          >
            待做
          </button>
          <button
            type="button"
            class="tab-btn"
            :class="{ active: filterType === 'done' }"
            :aria-pressed="filterType === 'done'"
            @click="filterType = 'done'"
          >
            已过
          </button>
        </div>

        <div class="count-stat font-mono">
          {{ passed }}<span class="total">/{{ total }}</span>
        </div>
      </div>
    </div>

    <!-- 题目列表（精排紧凑密度 + 吸顶章节头） -->
    <div ref="listRef" class="sidebar-list">
      <div v-if="filteredGroups.length === 0" class="empty-hint">
        <template v-if="loading && groups.length === 0">
          <p class="empty-hint-text">正在载入题库…</p>
        </template>
        <template v-else-if="error && groups.length === 0">
          <p class="empty-hint-text err">{{ error }}</p>
          <button type="button" class="btn btn-ghost empty-action-btn" @click="emit('retry')">
            重试
          </button>
        </template>
        <template v-else-if="groups.length === 0">
          <p class="empty-hint-text">当前方案暂无题目</p>
          <button type="button" class="btn btn-ghost empty-action-btn" @click="emit('open-course-modal')">
            切换方案
          </button>
        </template>
        <template v-else>
          <p class="empty-hint-text">没有匹配的题目</p>
          <button type="button" class="btn btn-ghost empty-action-btn" @click="resetFilters">
            重置筛选
          </button>
        </template>
      </div>

      <div v-for="g in filteredGroups" :key="g.chap" class="chapter-block">
        <button
          type="button"
          class="chapter-title"
          :aria-expanded="chapterExpanded(g.chap, g.items)"
          @click="toggleChapter(g.chap, g.items)"
        >
          <ChevronRight
            class="icon arrow"
            :size="10"
            :stroke-width="1.5"
            :style="{ transform: chapterExpanded(g.chap, g.items) ? 'rotate(90deg)' : 'none' }"
          />
          <span class="chap-name">第 {{ g.chap }} 章</span>
          <span class="chap-count font-mono">{{ g.passedInChap }}/{{ g.totalInChap }}</span>
        </button>

        <div v-show="chapterExpanded(g.chap, g.items)" class="problem-list">
          <button
            v-for="item in g.items"
            :key="item.eid"
            type="button"
            class="problem-row"
            :class="{ active: item.eid === activeEid }"
            :aria-current="item.eid === activeEid ? 'true' : undefined"
            @click="emit('select', item.eid)"
          >
            <span
              class="status-dot"
              :class="{ pass: item.pass, lab: item.questionType === 2 }"
            />
            <span class="pname">{{ item.pname }}</span>
            <span v-if="item.pmemo" class="pmemo">{{ item.pmemo }}</span>
          </button>
        </div>
      </div>
    </div>
    <!-- 底部账户区: 身份/方案 + 主题/设置/退出 (原顶栏入口收敛于此) -->
    <div class="sidebar-foot">
      <div class="identity">
        <button
          type="button"
          class="course-chip font-mono"
          :title="`方案 ${sid} / 班级 ${cid}，点击切换`"
          @click="emit('open-course-modal')"
        >
          {{ sid }}/{{ cid }}
        </button>
        <span class="student-name" :title="studentName || ''">{{ studentName || "未登录" }}</span>
      </div>

      <div class="foot-actions">
        <button
          type="button"
          class="btn-icon"
          :title="isDark ? '切换浅色模式' : '切换深色模式'"
          @click="emit('toggle-theme')"
        >
          <Sun v-if="isDark" class="icon" :size="15" :stroke-width="1.5" />
          <Moon v-else class="icon" :size="15" :stroke-width="1.5" />
        </button>

        <button type="button" class="btn-icon" title="偏好设置" @click="emit('open-settings')">
          <Settings class="icon" :size="15" :stroke-width="1.5" />
        </button>

        <button
          type="button"
          class="btn-icon"
          :title="studentName ? `退出 ${studentName}` : '退出'"
          @click="emit('logout')"
        >
          <LogOut class="icon" :size="15" :stroke-width="1.5" />
        </button>
      </div>
    </div>
  </aside>
</template>

<style scoped>
.sidebar {
  display: flex;
  flex-direction: column;
  height: 100%;
  background: var(--bg-surface);
  border-right: 1px solid var(--border-subtle);
  user-select: none;
  min-height: 0;
}

.sidebar-top {
  padding: 10px 12px 8px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  flex: none;
  border-bottom: 1px solid var(--border-subtle);
}

.search-row {
  display: flex;
  align-items: center;
  gap: 6px;
}

.search-wrap {
  position: relative;
  display: flex;
  align-items: center;
  flex: 1;
  min-width: 0;
}

.mobile-close-btn {
  display: none;
  flex: none;
}

.search-icon {
  position: absolute;
  left: 8px;
  color: var(--text-faint);
  pointer-events: none;
}

.search-input {
  width: 100%;
  height: 28px;
  background: var(--bg-canvas);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-control);
  padding: 0 24px 0 26px;
  font-size: 12px;
  color: var(--text-primary);
  outline: none;
  transition: border-color 0.15s ease;
}

.search-input:focus {
  border-color: var(--accent);
}

.search-input::placeholder {
  color: var(--text-muted);
}

.clear-btn {
  position: absolute;
  right: 5px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 18px;
  height: 18px;
  color: var(--text-muted);
  cursor: pointer;
  border-radius: 4px;
}

.clear-btn:hover {
  color: var(--text-primary);
  background: var(--bg-elevated);
}

.filter-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 11.5px;
  padding: 0 1px;
}

.filter-tabs {
  display: flex;
  align-items: center;
  gap: 10px;
}

.tab-btn {
  color: var(--text-muted);
  padding: 1px 0;
  transition: color 0.12s;
  line-height: 1.2;
}

.tab-btn:hover {
  color: var(--text-secondary);
}

.tab-btn.active {
  color: var(--text-primary);
  font-weight: 500;
}

.count-stat {
  color: var(--text-secondary);
  font-size: 11px;
}

.count-stat .total {
  color: var(--text-faint);
}

/* 列表滚动区 — 微弱滑块 + 丝滑吸顶 */
.sidebar-list {
  flex: 1;
  overflow-y: auto;
  min-height: 0;
  padding: 2px 0 12px;
  scrollbar-width: thin;
  scrollbar-color: transparent transparent;
}

.sidebar-list:hover {
  scrollbar-color: var(--border) transparent;
}

.sidebar-list::-webkit-scrollbar {
  width: 3px;
}

.sidebar-list::-webkit-scrollbar-thumb {
  background: transparent;
  border-radius: 2px;
}

.sidebar-list:hover::-webkit-scrollbar-thumb {
  background: var(--border);
}

.empty-hint {
  padding: 24px 16px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  text-align: center;
  color: var(--text-muted);
  font-size: 11.5px;
}

.empty-hint-text {
  margin: 0;
  line-height: 1.5;
  word-break: break-word;
}

.empty-hint-text.err {
  color: var(--err);
}

.empty-action-btn {
  font-size: 11.5px;
  padding: 3px 10px;
}

.chapter-block {
  margin-bottom: 2px;
}

/* 吸顶章节条 */
.chapter-title {
  position: sticky;
  top: 0;
  z-index: 2;
  width: 100%;
  background: var(--bg-surface);
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 12px;
  font-size: 10.5px;
  font-weight: 500;
  color: var(--text-muted);
  text-align: left;
  cursor: pointer;
  letter-spacing: 0.02em;
  transition: color 0.1s;
}

.chapter-title:hover {
  color: var(--text-secondary);
}

.arrow {
  color: var(--text-faint);
  transition: transform 0.15s ease;
}

.chap-name {
  flex: 1;
}

.chap-count {
  font-size: 9.5px;
  color: var(--text-muted);
  font-weight: 400;
}

.problem-list {
  display: flex;
  flex-direction: column;
  padding: 0 4px 2px;
}

/* 精准微排单行题目 */
.problem-row {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 3px 8px 3px 8px;
  min-height: 24px;
  text-align: left;
  border-radius: 4px;
  transition: background 0.08s ease;
  cursor: pointer;
}

.problem-row:hover {
  background: var(--bg-elevated);
}

.problem-row.active {
  background: var(--bg-elevated);
}

.problem-row.active .pname {
  color: var(--text-primary);
  font-weight: 500;
}

.status-dot {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--text-faint);
  opacity: 0.5;
  flex: none;
}

.status-dot.pass {
  background: var(--ok);
  opacity: 1;
}

/* 实验题是题型标记不是状态, 用中性色, 琥珀留给等待/调试语义 */
.status-dot.lab {
  background: var(--text-muted);
  opacity: 1;
}

.pname {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  font-size: 11.5px;
  color: var(--text-secondary);
  flex: none;
}

.pmemo {
  font-size: 11px;
  color: var(--text-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  margin-left: 2px;
  line-height: 1.2;
}

/* 高亮行底色变 elevated 时 muted 在浅色下踩线, 提一档 */
.problem-row:hover .pmemo,
.problem-row.active .pmemo {
  color: var(--text-secondary);
}

.status-strip {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  padding: 8px 12px 0;
  flex: none;
}

.status-item {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 11px;
  font-weight: 500;
  color: var(--text-muted);
}

.status-dot-inline {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--text-muted);
  flex: none;
}

.status-item.warn {
  color: var(--warn);
}

.status-item.warn .status-dot-inline {
  background: var(--warn);
}

.status-item.err {
  color: var(--err);
}

.status-item.err .status-dot-inline {
  background: var(--err);
}

/* 底部账户区 */
.sidebar-foot {
  flex: none;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border-top: 1px solid var(--border-subtle);
  background: var(--bg-surface);
}

.identity {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 1px;
  min-width: 0;
  flex: 1;
}

.course-chip {
  font-size: 10.5px;
  color: var(--text-muted);
  padding: 0 3px;
  border-radius: 4px;
  transition: color 0.12s;
}

.course-chip:hover {
  color: var(--text-primary);
  background: var(--bg-elevated);
}

.student-name {
  font-size: 12px;
  font-weight: 500;
  color: var(--text-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
  padding: 0 3px;
}

.foot-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  flex: none;
}

@media (max-width: 1200px) {
  .sidebar {
    position: fixed;
    top: 0;
    bottom: 0;
    left: 0;
    width: 280px;
    z-index: 50;
    transform: translateX(-100%);
    transition: transform 0.2s cubic-bezier(0.16, 1, 0.3, 1);
    box-shadow: none;
  }

  .sidebar.mobile-open {
    transform: translateX(0);
    box-shadow: var(--shadow-overlay);
  }

  .mobile-close-btn {
    display: inline-flex;
  }
}

@media (prefers-reduced-motion: reduce) {
  .sidebar,
  .arrow {
    transition: none;
  }
}

/* 触控设备人机工效优化：放大手指点选热区 */
@media (pointer: coarse) {
  .problem-row {
    min-height: 38px;
    padding: 7px 12px;
    gap: 10px;
  }
  .pname {
    font-size: 13px;
  }
  .pmemo {
    font-size: 12px;
  }
  .chapter-title {
    min-height: 36px;
    padding: 9px 12px;
    font-size: 12px;
  }
  .tab-btn {
    min-height: 36px;
    padding: 5px 12px;
    font-size: 12px;
    display: inline-flex;
    align-items: center;
  }
  .course-chip {
    min-height: 32px;
    display: inline-flex;
    align-items: center;
    padding: 2px 6px;
  }
  .clear-btn {
    width: 24px;
    height: 24px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
}
</style>
