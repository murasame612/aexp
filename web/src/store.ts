import { create } from "zustand";
import type { Locale, Run } from "./types";

const tokenKey = "aexp_api_token";
const localeKey = "aexp_locale";

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
  const saved = localStorage.getItem(localeKey);
  if (saved === "en" || saved === "zh") return saved;
  return navigator.language.toLowerCase().startsWith("zh") ? "zh" : "en";
}

export const useAppStore = create<AppState>((set) => ({
  token: localStorage.getItem(tokenKey) || "",
  locale: initialLocale(),
  selectedRunIds: new Set<string>(),
  setToken: (token) => {
    localStorage.setItem(tokenKey, token);
    set({ token });
  },
  clearToken: () => {
    localStorage.removeItem(tokenKey);
    set({ token: "" });
  },
  setLocale: (locale) => {
    localStorage.setItem(localeKey, locale);
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
