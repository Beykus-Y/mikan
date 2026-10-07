import { useEffect, useState } from "react";
import { t } from "../i18n";

export const THEMES = ["mikan", "midnight", "ocean", "sakura", "forest"] as const;
export type Theme = (typeof THEMES)[number];
const KEY = "mikan-theme";

function valid(value: string | null): value is Theme {
  return value !== null && (THEMES as readonly string[]).includes(value);
}

export function getTheme(): Theme {
  if (typeof window === "undefined") return "mikan";
  try {
    const value = window.localStorage.getItem(KEY);
    return valid(value) ? value : "mikan";
  } catch {
    return "mikan";
  }
}

export function setTheme(theme: Theme) {
  const next: Theme = valid(theme) ? theme : "mikan";
  document.documentElement.dataset.theme = next;
  try {
    window.localStorage.setItem(KEY, next);
  } catch {
    // Storage can be unavailable in private/restricted browser contexts.
  }
}

export function ThemeCard() {
  const [theme, set] = useState<Theme>(getTheme);

  useEffect(() => setTheme(theme), [theme]);

  const options: { id: Theme; label: string }[] = [
    { id: "mikan", label: t("settings.themeMikan") },
    { id: "midnight", label: t("settings.themeMidnight") },
    { id: "ocean", label: t("settings.themeOcean") },
    { id: "sakura", label: t("settings.themeSakura") },
    { id: "forest", label: t("settings.themeForest") },
  ];

  return (
    <section className="card glass reveal" style={{ "--i": 4 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.theme")}</h2>
          <div className="card-sub">{t("settings.themeSub")}</div>
        </div>
      </div>
      <div className="theme-grid" role="radiogroup" aria-label={t("settings.theme")}>
        {options.map((option) => (
          <button
            key={option.id}
            type="button"
            role="radio"
            aria-checked={theme === option.id}
            className="theme-option"
            data-theme-preview={option.id}
            onClick={() => set(option.id)}
          >
            <span className="theme-swatch" aria-hidden />
            <span>{option.label}</span>
          </button>
        ))}
      </div>
    </section>
  );
}
