<script setup lang="ts">
import { Eye, EyeOff } from "lucide-vue-next";
import { useToast } from "~/composables/useToast";

const api = useApi();
const toast = useToast();

const username = ref("");
const password = ref("");
const showPassword = ref(false);
const busy = ref(false);
const error = ref("");
const relayState = ref<"checking" | "ok" | "mock" | "unreachable">("checking");

async function checkRelay() {
  relayState.value = "checking";
  try {
    const h = await api.health();
    relayState.value = h.mock ? "mock" : "ok";
  } catch {
    relayState.value = "unreachable";
  }
}

onMounted(() => {
  checkRelay();
});

async function submit() {
  const user = username.value.trim();
  if (!user || !password.value || busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    const res = await api.login(user, password.value);
    toast.success(`欢迎, ${res.student?.name || user}`);
    navigateTo("/");
  } catch (e: any) {
    const raw = String(e?.message ?? "");
    let msg = raw;
    // 上游偶发吐 JSON 体，只留 message 字段；握手失败统一人话
    const jm = raw.match(/"message"\s*:\s*"([^"]+)"/);
    const extracted = jm?.[1];
    if (extracted) {
      msg = extracted.includes("expired") || extracted.includes("JWT")
        ? "登录会话已过期，请重试"
        : extracted;
    } else if (raw.includes("取公钥失败")) {
      msg = "无法建立加密握手，请确认网络后重试";
    } else if (e?.status === 0 || /failed to fetch|networkerror|load failed/i.test(raw)) {
      msg = "无法连接 relay 服务，请确认后端已启动或反代 /api 配置正确";
      relayState.value = "unreachable";
    }
    error.value = msg || "登录失败，请检查学号与密码";
    toast.error(error.value);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <div class="login-wrapper">
    <div class="login-card">
      <div class="card-head">
        <h1 class="brand-title">AnyView</h1>
        <p class="brand-desc">自托管编程工作台</p>
      </div>

      <div v-if="relayState === 'mock'" class="relay-status mock" role="status">
        <span class="status-dot" />
        <span>Mock 演示模式，可任意输入学号与密码</span>
      </div>

      <div v-else-if="relayState === 'unreachable'" class="relay-status unreachable" role="alert">
        <span class="status-dot err" />
        <span class="status-text">无法连接 relay 服务，请确认后端已启动</span>
        <button type="button" class="btn btn-ghost retry-btn" @click="checkRelay">
          重试
        </button>
      </div>

      <form class="login-form" @submit.prevent="submit">
        <div class="input-group">
          <div class="input-row">
            <label class="sr-only" for="login-username">学号</label>
            <input
              id="login-username"
              v-model="username"
              type="text"
              inputmode="numeric"
              autocomplete="username"
              placeholder="学号"
              autofocus
              required
            >
          </div>
          <div class="input-row password-row">
            <label class="sr-only" for="login-password">密码</label>
            <input
              id="login-password"
              v-model="password"
              :type="showPassword ? 'text' : 'password'"
              autocomplete="current-password"
              placeholder="密码"
              required
            >
            <button
              type="button"
              class="btn-icon toggle-pw-btn"
              :aria-label="showPassword ? '隐藏密码' : '显示密码'"
              :aria-pressed="showPassword"
              @click="showPassword = !showPassword"
            >
              <EyeOff v-if="showPassword" class="icon" :size="14" :stroke-width="1.5" />
              <Eye v-else class="icon" :size="14" :stroke-width="1.5" />
            </button>
          </div>
        </div>

        <div v-if="error" class="error-line" role="alert">
          {{ error }}
        </div>

        <button
          type="submit"
          class="btn btn-primary btn-submit"
          :disabled="busy || !username.trim() || !password"
          :aria-busy="busy || undefined"
        >
          {{ busy ? "登录中" : "登录" }}
        </button>

        <p class="hint">密码经 relay 加密转发，不落盘。</p>
      </form>
    </div>
    <Toast />
  </div>
</template>

<style scoped>
.login-wrapper {
  height: 100%;
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--bg-canvas);
  padding: 20px;
}

.login-card {
  width: 100%;
  max-width: 280px;
  display: flex;
  flex-direction: column;
  gap: 28px;
}

.card-head {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.brand-title {
  font-size: 20px;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0;
  letter-spacing: -0.01em;
}

.brand-desc {
  font-size: 12.5px;
  color: var(--text-muted);
  margin: 0;
}

.login-form {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.input-group {
  display: flex;
  flex-direction: column;
  background: var(--bg-canvas);
  border: 1px solid var(--border);
  border-radius: var(--radius-control);
  overflow: hidden;
  transition: border-color 0.15s ease;
}

.input-group:focus-within {
  border-color: var(--accent);
}

.input-row {
  display: flex;
  align-items: center;
}

.input-row:first-child {
  border-bottom: 1px solid var(--border-subtle);
}

.input-row input {
  flex: 1;
  min-width: 0;
  padding: 10px 13px;
  background: transparent;
  border: none;
  font-size: 13.5px;
  color: var(--text-primary);
  outline: none;
}

.password-row input {
  padding-right: 4px;
}

.toggle-pw-btn {
  margin-right: 6px;
  flex: none;
}

.input-row input::placeholder {
  color: var(--text-muted);
}

.relay-status {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-muted);
}

.relay-status.unreachable {
  color: var(--err);
}

.relay-status .status-text {
  flex: 1;
}

.status-dot {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--text-muted);
  flex: none;
}

.status-dot.err {
  background: var(--err);
}

.retry-btn {
  padding: 2px 8px;
  font-size: 11.5px;
  flex: none;
}

.error-line {
  font-size: 12px;
  line-height: 1.5;
  color: var(--err);
}

.btn-submit {
  width: 100%;
  padding: 9px;
  font-size: 13px;
}

.hint {
  font-size: 11.5px;
  color: var(--text-muted);
  line-height: 1.5;
  margin: 0;
}
</style>
