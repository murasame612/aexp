import { create } from "zustand";
import type { Locale, Run } from "./types";

const tokenKey = "aexp_api_token";
const localeKey = "aexp_locale";

const safeStorage = {
  get(key:string) { try { return window.localStorage.getItem(key); } catch { return null; } },
  set(key:string,value:string) { try { window.localStorage.setItem(key,value); } catch { /* in-memory state still works */ } },
  remove(key:string) { try { window.localStorage.removeItem(key); } catch { /* in-memory state still works */ } }
};

interface AppState {
  token: string;
  locale: Locale;
  selectedRunIds: Set<string>;
  setToken: (token: string) => void;
  clearToken: () => void;
  setLocale: (locale: Locale) => void;
  toggleSelectedRun: (run: Run, checked: boolean) => void;
  clearSelectedRuns: () => void;
}

function initialLocale(): Locale {
  const saved = safeStorage.get(localeKey);
  if (saved === "en" || saved === "zh") return saved;
  return navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";
}

export const useAppStore = create<AppState>((set) => ({
  token: safeStorage.get(tokenKey) || "",
  locale: initialLocale(),
  selectedRunIds: new Set<string>(),
  setToken: (token) => {
    safeStorage.set(tokenKey, token);
    set({ token });
  },
  clearToken: () => {
    safeStorage.remove(tokenKey);
    set({ token: "" });
  },
  setLocale: (locale) => {
    safeStorage.set(localeKey, locale);
    set({ locale });
  },
  toggleSelectedRun: (run, checked) =>
    set((state) => {
      const selectedRunIds = new Set(state.selectedRunIds);
      if (checked) selectedRunIds.add(run.id);
      else selectedRunIds.delete(run.id);
      return { selectedRunIds };
    }),
  clearSelectedRuns: () => set({ selectedRunIds: new Set<string>() })
}));
