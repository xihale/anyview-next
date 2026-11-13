// 暗/浅主题: localStorage an.theme + html[data-theme] 同步(自 index.vue 抽出)
import { onMounted, ref } from "vue";

// 浏览器 chrome 色跟手: data-theme 与 theme-color meta 同步,
// app.vue 冷启动与侧栏切换都走这里, 两处不会各改一半
export function syncTheme(theme: string) {
  if (typeof document === "undefined") return;
  document.documentElement.setAttribute("data-theme", theme);
  document
    .querySelector('meta[name="theme-color"]')
    ?.setAttribute("content", theme === "light" ? "#f6f7f8" : "#101114");
}

export function useTheme() {
  const isDark = ref(true);

  function apply(theme: string) {
    isDark.value = theme === "dark";
    syncTheme(theme);
  }

  onMounted(() => {
    let saved = "dark";
    try {
      saved = localStorage.getItem("an.theme") || "dark";
    } catch {}
    apply(saved);
  });

  function toggleTheme() {
    const theme = isDark.value ? "light" : "dark";
    try {
      localStorage.setItem("an.theme", theme);
    } catch {}
    apply(theme);
  }

  return { isDark, toggleTheme };
}
