import { useState, type FormEvent } from "react";
import { ArrowDown, ArrowUp } from "lucide-react";
import type { Schemas } from "../../../api/client";
import { useInbounds, useNodes } from "../../../api/hooks";
import { Button, Field, Pill } from "../../../components/ui";
import { Switch } from "../../../components/switch";
import { t, tMaybe } from "../../../i18n";
import { useDraft } from "../../../lib/draft";
import { fieldErrors } from "../../../lib/fields";
import { FingerprintSelect } from "../../../components/fingerprint-select";
import { useSaveSettings } from "./shared";

// Ports the installer opens in the firewall (443 and the HTTPS pool): a subscription
// port among them needs nothing else on the server.
const OPEN_PORTS = [443, 2053, 2083, 2087, 2096, 8443] as const;

// Subscriptions on a port of their own: a usual HTTPS port looks like any site, and the
// admin panel's port stops showing in every link. Links on the old port keep working.
export function SubPortCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const inbounds = useInbounds();
  const nodes = useNodes();
  const { draft: port, setDraft: setPort } = useDraft(s.sub_port ? String(s.sub_port) : "");
  const error = fieldErrors(save.error).sub_port;
  const own = nodes.data?.find((n) => n.local)?.id;
  // Who holds a port over TCP on the panel's own server.
  const holder = (p: number) => (inbounds.data ?? []).find((i) => i.node_id === own && i.enabled && i.network === "tcp" && i.port === String(p))?.name;
  const value = Number(port);
  const valid = port.trim() === "" || (Number.isInteger(value) && value >= 1 && value <= 65535);
  const next = port.trim() === "" ? 0 : value;
  const changed = next !== (s.sub_port ?? 0);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (valid) save.mutate({ sub_port: next });
  };
  const current = s.sub_port || s.panel_port;
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.subPort")}</h2>
            <div className="card-sub">{t("settings.subPortSub")}</div>
          </div>
          {s.sub_port ? <Pill tone={s.sub_port_error ? "bad" : "ok"}>{s.sub_port_error ? t("settings.subPortDown") : t("settings.subPortOn", { port: s.sub_port })}</Pill> : null}
        </div>
        {s.sub_port_error ? (
          <div className="banner err mb-4" role="alert">
            {t("settings.subPortBusy", { port: s.sub_port, panel: s.panel_port })}
          </div>
        ) : null}
        {s.sub_base_url ? (
          <div className="panel-soft mb-4 p-3">
            <div className="text-xs text-[var(--ink-500)]">{t("settings.subPortLinks")}</div>
            <div className="mono truncate text-[13px]">{s.sub_base_url.replace(/[^/]+\/$/, "…")}</div>
          </div>
        ) : null}
        <Field label={t("settings.subPortField")} htmlFor="s-sub-port" hint={t("settings.subPortHint", { panel: s.panel_port })} error={error ?? (valid ? undefined : t("settings.subPortInvalid"))}>
          <input
            id="s-sub-port"
            className="input mono max-w-[140px]"
            inputMode="numeric"
            value={port}
            onChange={(e) => setPort(e.target.value.replace(/[^0-9]/g, ""))}
            placeholder={String(s.panel_port)}
            aria-invalid={!!error || !valid}
          />
        </Field>
        <div className="mb-4 flex flex-wrap gap-2" role="group" aria-label={t("settings.subPortQuick")}>
          {OPEN_PORTS.map((p) => {
            const who = p === s.panel_port ? t("settings.subPortPanel") : holder(p);
            return (
              <button
                key={p}
                type="button"
                className="chip"
                aria-pressed={port === String(p)}
                disabled={!!who}
                title={p === s.panel_port ? t("errors.api.sub_port_panel") : who ? t("settings.subPortHeld", { name: who }) : undefined}
                onClick={() => setPort(String(p))}
              >
                <span className="mono">{p}</span>
                {who ? <span className="text-[var(--ink-400)]"> · {who}</span> : null}
              </button>
            );
          })}
        </div>
        <p className="mb-4 text-xs text-[var(--ink-500)]">{t("settings.subPortNote")}</p>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" variant="primary" loading={save.isPending && save.variables?.sub_port === next} disabled={!changed || !valid}>
            {t("common.save")}
          </Button>
          {s.sub_port ? (
            <Button variant="ghost" loading={save.isPending && save.variables?.sub_port === 0} onClick={() => save.mutate({ sub_port: 0 })}>
              {t("settings.subPortOff", { port: current === s.sub_port ? s.panel_port : current })}
            </Button>
          ) : null}
        </div>
      </form>
    </section>
  );
}

const routingModes = [
  { id: "ru_direct", title: "settings.routingRuDirect", sub: "settings.routingRuDirectSub" },
  { id: "all", title: "settings.routingAll", sub: "settings.routingAllSub" },
] as const;

export function SubscriptionCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const inbounds = useInbounds();
  const { draft: form, setDraft: setForm } = useDraft({ brand: s.brand, support_url: s.support_url, sub_group_main: s.sub_group_main, sub_group_auto: s.sub_group_auto, sub_routing: s.sub_routing, client_fingerprint: s.client_fingerprint });
  const [fpOk, setFpOk] = useState(true);
  const errors = fieldErrors(save.error);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ brand: form.brand, support_url: form.support_url, sub_group_main: form.sub_group_main.trim(), sub_group_auto: form.sub_group_auto.trim(), sub_routing: form.sub_routing, client_fingerprint: form.client_fingerprint });
  };
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm((f) => ({ ...f, [k]: e.target.value }));
  const proxies = (inbounds.data ?? []).filter((i) => i.enabled).map((i) => i.sub_name);
  return (
    <section className="card glass reveal" style={{ "--i": 1 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.subscription")}</h2>
            <div className="card-sub">{t("settings.subscriptionSub")}</div>
          </div>
        </div>
        <Field label={t("settings.brand")} htmlFor="s-brand" hint={t("settings.brandHint")}>
          <input id="s-brand" className="input" value={form.brand} onChange={set("brand")} maxLength={40} />
        </Field>
        <div className="grid gap-x-3 sm:grid-cols-2">
          <Field label={t("settings.groupMain")} htmlFor="s-gmain" hint={t("settings.groupMainHint")} error={errors.sub_group_main}>
            <input id="s-gmain" className="input" value={form.sub_group_main} onChange={set("sub_group_main")} maxLength={48} aria-invalid={!!errors.sub_group_main} autoComplete="off" />
          </Field>
          <Field label={t("settings.groupAuto")} htmlFor="s-gauto" hint={t("settings.groupAutoHint")} error={errors.sub_group_auto}>
            <input id="s-gauto" className="input" value={form.sub_group_auto} onChange={set("sub_group_auto")} maxLength={48} aria-invalid={!!errors.sub_group_auto} autoComplete="off" />
          </Field>
        </div>
        <div className="panel-soft mb-4 p-3" aria-label={t("settings.preview")}>
          <div className="mb-2 text-xs text-[var(--ink-500)]">{t("settings.previewHint")}</div>
          <div className="flex items-center gap-2">
            <b className="truncate text-[13px]">{form.sub_group_main || "—"}</b>
            <span className="rounded-md border border-[var(--hairline)] px-1.5 py-0.5 text-[10px] font-semibold tracking-wide text-[var(--ink-500)]">SELECTOR</span>
          </div>
          <div className="mt-2 flex flex-wrap gap-1.5">
            <span className="tag inline-flex items-center gap-1">
              {form.sub_group_auto || "—"}
              <span className="text-[10px] font-semibold tracking-wide text-[var(--ink-400)]">URLTEST</span>
            </span>
            {proxies.map((p) => (
              <span key={p} className="tag">
                {p}
              </span>
            ))}
          </div>
        </div>
        <Field label={t("settings.routing")} hint={t("settings.routingHint")}>
          <div className="grid gap-2 sm:grid-cols-2" role="radiogroup" aria-label={t("settings.routing")}>
            {routingModes.map((m) => (
              <button key={m.id} type="button" role="radio" aria-checked={form.sub_routing === m.id} className="opt" onClick={() => setForm((f) => ({ ...f, sub_routing: m.id }))}>
                <span className="font-semibold">{t(m.title)}</span>
                <span className="text-xs text-[var(--ink-500)]">{t(m.sub)}</span>
              </button>
            ))}
          </div>
        </Field>
        <Field label={t("settings.fingerprint")} htmlFor="s-fp" hint={t("settings.fingerprintHint")} error={errors.client_fingerprint}>
          <FingerprintSelect key={s.client_fingerprint} id="s-fp" value={form.client_fingerprint} onChange={(v) => setForm((f) => ({ ...f, client_fingerprint: v }))} invalid={!!errors.client_fingerprint} onValid={setFpOk} />
        </Field>
        <Field label={t("settings.support")} htmlFor="s-support" hint={t("settings.supportHint")} error={errors.support_url}>
          <input id="s-support" className="input" value={form.support_url} onChange={set("support_url")} placeholder="https://t.me/your_support" aria-invalid={!!errors.support_url} />
        </Field>
        <Button type="submit" variant="primary" loading={save.isPending} disabled={!fpOk || !form.client_fingerprint}>
          {t("common.save")}
        </Button>
      </form>
    </section>
  );
}

// The variables the title and the announcement take (subs.TitleVars).
const TITLE_VARS = ["brand", "name", "date", "days", "used", "left", "total"] as const;

// The {words} of a text that are no variable: they stay as text in the apps, so this only
// warns (same case-sensitive match as the panel's substitution).
function unknownVars(text: string): string[] {
  const found = new Set<string>();
  for (const m of text.matchAll(/\{([A-Za-z0-9_-]+)\}/g)) {
    if (!(TITLE_VARS as readonly string[]).includes(m[1] ?? "")) found.add(m[0]);
  }
  return [...found];
}

// What the apps show besides the servers: an announcement for every app that reads one,
// and the brand for the apps that read operator headers (ClashFest, SlothClash).
export function AppsCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const { draft: form, setDraft: setForm } = useDraft({ sub_title: s.sub_title, sub_announce: s.sub_announce, sub_announce_url: s.sub_announce_url, brand_accent: s.brand_accent, brand_logo_url: s.brand_logo_url });
  const errors = fieldErrors(save.error);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ sub_title: form.sub_title.trim(), sub_announce: form.sub_announce.trim(), sub_announce_url: form.sub_announce_url.trim(), brand_accent: form.brand_accent.trim(), brand_logo_url: form.brand_logo_url.trim() });
  };
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm((f) => ({ ...f, [k]: e.target.value }));
  // A variable chip goes into the field last typed in: the title until the announcement is.
  const [target, setTarget] = useState<"sub_title" | "sub_announce">("sub_title");
  const insert = (v: string) => setForm((f) => ({ ...f, [target]: (f[target] ? f[target].replace(/\s*$/, " ") : "") + `{${v}}` }));
  const unknownTitle = unknownVars(form.sub_title);
  const unknownAnnounce = unknownVars(form.sub_announce);
  const withUnknown = (hint: string, unknown: string[]) =>
    unknown.length === 0 ? (
      hint
    ) : (
      <>
        {hint} <span className="font-medium">{t("settings.unknownVars", { vars: unknown.join(" ") })}</span>
      </>
    );
  const accent = /^#[0-9A-Fa-f]{6}$/.test(form.brand_accent.trim()) ? form.brand_accent.trim() : "";
  return (
    <section className="card glass reveal" style={{ "--i": 2 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("settings.apps")}</h2>
            <div className="card-sub">{t("settings.appsSub")}</div>
          </div>
        </div>
        <Field label={t("settings.subTitle")} htmlFor="s-sub-title" hint={withUnknown(t("settings.subTitleHint"), unknownTitle)} error={errors.sub_title}>
          <input
            id="s-sub-title"
            className="input"
            value={form.sub_title}
            onChange={set("sub_title")}
            onFocus={() => setTarget("sub_title")}
            maxLength={200}
            placeholder={s.brand || "{brand}"}
            aria-invalid={!!errors.sub_title}
          />
        </Field>
        <Field label={t("settings.announce")} htmlFor="s-announce" hint={withUnknown(t("settings.announceHint"), unknownAnnounce)} error={errors.sub_announce}>
          <input
            id="s-announce"
            className="input"
            value={form.sub_announce}
            onChange={set("sub_announce")}
            onFocus={() => setTarget("sub_announce")}
            maxLength={200}
            aria-invalid={!!errors.sub_announce}
          />
        </Field>
        <div className="-mt-1 mb-3 flex flex-wrap items-center gap-2" role="group" aria-label={t("settings.titleVars")}>
          <span className="text-xs text-[var(--ink-500)]">{t("settings.titleVars")}</span>
          {TITLE_VARS.map((v) => (
            <button key={v} type="button" className="chip-btn" title={t(`settings.titleVar.${v}`)} onClick={() => insert(v)}>
              <span className="mono">{`{${v}}`}</span>
            </button>
          ))}
        </div>
        <Field label={t("settings.announceUrl")} htmlFor="s-announce-url" hint={t("settings.announceUrlHint")} error={errors.sub_announce_url}>
          <input id="s-announce-url" className="input" value={form.sub_announce_url} onChange={set("sub_announce_url")} placeholder="https://t.me/your_channel" aria-invalid={!!errors.sub_announce_url} />
        </Field>
        <div className="flex items-start justify-between gap-4 py-3">
          <div className="min-w-0">
            <div className="text-[13px] font-medium">{t("settings.appBranding")}</div>
            <div className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.appBrandingSub")}</div>
          </div>
          <Switch checked={s.app_branding} label={t("settings.appBranding")} disabled={save.isPending} onChange={(v) => save.mutate({ app_branding: v })} />
        </div>
        {s.app_branding ? (
          <div className="grid gap-x-3 sm:grid-cols-[160px_1fr]">
            <Field label={t("settings.brandAccent")} htmlFor="s-accent" hint={t("settings.brandAccentHint")} error={errors.brand_accent}>
              <div className="flex items-center gap-2">
                <span className="h-9 w-9 shrink-0 rounded-lg border border-[var(--hairline)]" style={{ background: accent || "transparent" }} aria-hidden />
                <input id="s-accent" className="input mono" value={form.brand_accent} onChange={set("brand_accent")} maxLength={7} placeholder="#F07A2E" autoComplete="off" aria-invalid={!!errors.brand_accent} />
              </div>
            </Field>
            <Field label={t("settings.brandLogo")} htmlFor="s-logo" hint={t("settings.brandLogoHint")} error={errors.brand_logo_url}>
              <input id="s-logo" className="input" value={form.brand_logo_url} onChange={set("brand_logo_url")} placeholder="https://example.com/logo-256.png" aria-invalid={!!errors.brand_logo_url} />
            </Field>
          </div>
        ) : null}
        <Button type="submit" variant="primary" loading={save.isPending}>
          {t("common.save")}
        </Button>
      </form>
    </section>
  );
}

const PAGE_THEMES = ["mikan", "midnight", "ocean", "sakura", "forest"] as const;
const PAGE_THEME_LABELS = { mikan: "settings.themeMikan", midnight: "settings.themeMidnight", ocean: "settings.themeOcean", sakura: "settings.themeSakura", forest: "settings.themeForest" } as const;
const MODULE_LABELS: Record<string, string> = {
  status: "settings.pageModuleStatus", usage: "settings.pageModuleUsage", pools: "settings.pageModulePools",
  devices: "settings.pageModuleDevices", apps: "settings.pageModuleApps", instructions: "settings.pageModuleInstructions",
  link: "settings.pageModuleLink", promo: "settings.pageModulePromo", shop: "settings.pageModuleShop",
  packages: "settings.pageModulePackages", telegram: "settings.pageModuleTelegram", support: "settings.pageModuleSupport",
};

/** Theme, logo and the order/visibility of sections on the public subscription page. */
export function SubPageCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  const [theme, setTheme] = useState(s.subscription_theme);
  const [logo, setLogo] = useState(s.subscription_logo);
  const [modules, setModules] = useState(s.subscription_modules);
  const errors = fieldErrors(save.error);
  const move = (index: number, delta: number) => {
    const next = [...modules];
    const target = index + delta;
    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target]!, next[index]!];
    setModules(next);
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate({ subscription_theme: theme, subscription_logo: logo.trim(), subscription_modules: modules });
  };
  return (
    <section className="card glass reveal" style={{ "--i": 2 } as React.CSSProperties}>
      <form onSubmit={submit} noValidate>
        <div className="card-head"><div><h2 className="card-title">{t("settings.subscriptionPage")}</h2><div className="card-sub">{t("settings.subscriptionPageSub")}</div></div></div>
        <Field label={t("settings.subscriptionTheme")} htmlFor="sub-page-theme" error={errors.subscription_theme}>
          <select id="sub-page-theme" className="input" value={theme} onChange={(e) => setTheme(e.target.value as typeof theme)}>
            {PAGE_THEMES.map((value) => <option key={value} value={value}>{t(PAGE_THEME_LABELS[value])}</option>)}
          </select>
        </Field>
        <Field label={t("settings.subscriptionLogo")} htmlFor="sub-page-logo" hint={t("settings.subscriptionLogoHint")} error={errors.subscription_logo}>
          <div className="flex items-center gap-3">
            <span className="grid h-10 w-10 shrink-0 place-items-center overflow-hidden rounded-xl border border-[var(--hairline)] bg-[var(--surface-soft)] text-xl">
              {logo.startsWith("https://") ? <img src={logo} alt="" className="h-full w-full object-cover" /> : logo || (s.brand || "V")[0]}
            </span>
            <input id="sub-page-logo" className="input" value={logo} onChange={(e) => setLogo(e.target.value)} maxLength={500} placeholder="✨ или https://example.com/logo.png" aria-invalid={!!errors.subscription_logo} />
          </div>
        </Field>
        <div className="mt-4 mb-2 text-[13px] font-medium">{t("settings.subscriptionModules")}</div>
        <div className="row-list">
          {modules.map((module, index) => (
            <div key={module.id} className="flex items-center gap-2 py-2">
              <div className="flex min-w-0 flex-1 items-center gap-3">
                <Switch checked={module.enabled} label={tMaybe(MODULE_LABELS[module.id] ?? module.id) ?? module.id} disabled={save.isPending} onChange={(enabled) => setModules((cur) => cur.map((m) => m.id === module.id ? { ...m, enabled } : m))} />
                <span className="truncate text-[13px]">{tMaybe(MODULE_LABELS[module.id] ?? module.id) ?? module.id}</span>
              </div>
              <button type="button" className="icon-btn" aria-label={t("settings.moduleMoveUp")} disabled={index === 0 || save.isPending} onClick={() => move(index, -1)}><ArrowUp size={16} /></button>
              <button type="button" className="icon-btn" aria-label={t("settings.moduleMoveDown")} disabled={index === modules.length - 1 || save.isPending} onClick={() => move(index, 1)}><ArrowDown size={16} /></button>
            </div>
          ))}
        </div>
        {errors.subscription_modules ? <div className="mt-2 text-xs text-[var(--berry-600)]">{errors.subscription_modules}</div> : null}
        <Button type="submit" variant="primary" loading={save.isPending}>
          {t("common.save")}
        </Button>
      </form>
    </section>
  );
}

export function DevicesCard({ s }: { s: Schemas["SettingsView"] }) {
  const save = useSaveSettings();
  return (
    <section className="card glass reveal" style={{ "--i": 4 } as React.CSSProperties}>
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("settings.devices")}</h2>
          <div className="card-sub">{t("settings.devicesSub")}</div>
        </div>
      </div>
      <ul className="row-list">
        <li className="flex items-start justify-between gap-4 py-3">
          <div className="min-w-0">
            <div className="text-[13px] font-medium">{t("settings.binding")}</div>
            <div className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.bindingSub")}</div>
          </div>
          <Switch checked={s.device_binding} label={t("settings.binding")} disabled={save.isPending} onChange={(v) => save.mutate({ device_binding: v })} />
        </li>
        <li className="flex items-start justify-between gap-4 py-3">
          <div className="min-w-0">
            <div className="text-[13px] font-medium">{t("settings.requireHwid")}</div>
            <div className="mt-1 text-xs text-[var(--ink-500)]">{t("settings.requireHwidSub")}</div>
          </div>
          <Switch checked={s.device_require_hwid} label={t("settings.requireHwid")} disabled={save.isPending || !s.device_binding} onChange={(v) => save.mutate({ device_require_hwid: v })} />
        </li>
      </ul>
      <p className="mt-3 text-xs text-[var(--ink-500)]">{t("settings.devicesNote")}</p>
    </section>
  );
}
