<script setup lang="ts">
import { syncTheme } from "~/composables/useTheme";

useSettings();

if (import.meta.client) {
  try {
    syncTheme(localStorage.getItem("an.theme") || "dark");
  } catch {}
}

onMounted(() => {
  try {
    syncTheme(localStorage.getItem("an.theme") || "dark");
  } catch {}
});
</script>

<template>
  <NuxtPage />
</template>
