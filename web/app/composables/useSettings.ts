import { reactive, watch } from "vue";

export interface SettingsState {
  editorFontSize: number;
  editorTabSize: number;
  editorLineWrapping: boolean;
  statementFontSize: number;
}

const SETTINGS_REV = 3;

const DEFAULT_SETTINGS: SettingsState = {
  editorFontSize: 15,
  editorTabSize: 4,
  editorLineWrapping: false,
  statementFontSize: 14.5,
};

const settings = reactive<SettingsState>({ ...DEFAULT_SETTINGS });
let initialized = false;

// 只认白名单字段: 旧版本设置(zen/密度/强调色等已砍项)读进来直接丢弃, 不会再写回
function hydrate(parsed: Record<string, unknown>) {
  for (const key of Object.keys(DEFAULT_SETTINGS) as (keyof SettingsState)[]) {
    const v = parsed[key];
    if (v !== undefined && typeof v === typeof DEFAULT_SETTINGS[key]) {
      (settings as Record<string, unknown>)[key] = v;
    }
  }
}

function applyDom() {
  if (typeof document === "undefined") return;
  document.documentElement.style.setProperty("--editor-font-size", `${settings.editorFontSize}px`);
}

export function useSettings() {
  if (!initialized && typeof document !== "undefined") {
    if (typeof localStorage !== "undefined") {
      try {
        const raw = localStorage.getItem("an.settings");
        if (raw) {
          hydrate(JSON.parse(raw));
        }
      } catch {}
    }

    applyDom();

    watch(
      settings,
      (val) => {
        applyDom();
        if (typeof localStorage === "undefined") return;
        try {
          localStorage.setItem("an.settings", JSON.stringify({ ...val, _rev: SETTINGS_REV }));
        } catch {}
      },
      { deep: true },
    );

    initialized = true;
  }

  function reset() {
    Object.assign(settings, DEFAULT_SETTINGS);
  }

  return {
    settings,
    reset,
  };
}
