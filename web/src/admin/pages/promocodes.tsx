import { useMemo, useState } from "react";
import { Plus, Power, Trash2 } from "lucide-react";
import { errorText } from "../../api/client";
import { usePools, usePromoMutations, usePromoRedemptions, usePromocodes, useTariffs, type PromoCode, type PromoRedemption } from "../../api/hooks";
import { useToast } from "../../components/toast";
import { Button, ErrorState, PageHeader, Pill } from "../../components/ui";
import { t, tMaybe } from "../../i18n";
import { money } from "../../lib/format";

const empty: PromoCode = {
  id: 0, code: "", name: "", description: "", type: "days", value: 30, currency: "", used_count: 0,
  per_user_limit: 1, discount_ttl: 0, min_order: 0, max_discount: 0, tariff_ids: [], first_purchase_only: false,
  new_users_only: false, enabled: true, status: "active", created_at: "",
};

type Draft = Omit<PromoCode, "created_at" | "created_by" | "min_order" | "max_discount"> & {
  starts_at: string; ends_at: string; max_uses: string | number | undefined; value: string | number; min_order: string | number; max_discount: string | number;
};

// Sums are kept in the payment's smallest unit (kopecks, Stars); the form shows rubles.
function fromUnits(n: number, currency: string) {
  return currency === "RUB" ? n / 100 : n;
}

function toUnits(v: string | number, currency: string) {
  const n = Number(v) || 0;
  return Math.round(currency === "RUB" ? n * 100 : n);
}

function unitOf(currency: string) {
  return currency === "RUB" ? "₽" : "⭐";
}

function dateInput(value?: string) {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function epoch(value?: string) {
  if (!value) return undefined;
  const ms = new Date(value).getTime();
  return Number.isFinite(ms) ? Math.floor(ms / 1000) : undefined;
}

function statusTone(status: string): "ok" | "off" | "warn" {
  return status === "active" ? "ok" : status === "disabled" ? "off" : "warn";
}

function statusText(status: string) {
  switch (status) {
    case "active": return t("promocodes.status.active");
    case "inactive": return t("promocodes.status.inactive");
    case "expired": return t("promocodes.status.expired");
    case "exhausted": return t("promocodes.status.exhausted");
    case "disabled": return t("promocodes.status.disabled");
    case "deleted": return t("promocodes.status.deleted");
    default: return status;
  }
}

function redemptionStatusText(status: string) {
  return tMaybe(`promocodes.historyStatus.${status}`) ?? status;
}

function valueText(p: PromoCode) {
  if (p.type === "traffic") return `${(p.value / 1073741824).toFixed(1)} ${t("promocodes.trafficUnit")}`;
  if (p.type === "days") return `${p.value} ${t("promocodes.daysUnit")}`;
  return p.type === "percent" ? `${p.value}%` : money(p.value, p.currency);
}

export function PromocodesPage() {
  const toast = useToast();
  const codes = usePromocodes();
  const history = usePromoRedemptions();
  const { create, update, toggle, remove } = usePromoMutations();
  const [edit, setEdit] = useState<PromoCode | null>(null);
  const [tab, setTab] = useState<"codes" | "history">("codes");
  const [search, setSearch] = useState("");

  const items = codes.data?.items ?? [];
  const redemptions = history.data?.items ?? [];
  const filtered = useMemo(() => redemptions.filter((r: PromoRedemption) => !search || r.code.toLowerCase().includes(search.toLowerCase()) || String(r.user_id ?? "").includes(search)), [redemptions, search]);
  const loading = codes.isLoading || history.isLoading;

  const save = async (p: Omit<PromoCode, "created_at" | "created_by">) => {
    const body = {
      code: p.code, name: p.name, description: p.description, type: p.type, value: Number(p.value),
      currency: p.type === "fixed" || p.type === "percent" ? p.currency : "",
      starts_at: epoch(p.starts_at), ends_at: epoch(p.ends_at), max_uses: p.max_uses ? Number(p.max_uses) : undefined,
      per_user_limit: Number(p.per_user_limit) || 1, discount_ttl: Number(p.discount_ttl) || 0,
      min_order: Number(p.min_order) || 0, max_discount: Number(p.max_discount) || 0, tariff_ids: p.tariff_ids,
      pool_id: p.type === "traffic" ? p.pool_id : undefined,
      first_purchase_only: p.first_purchase_only, new_users_only: p.new_users_only, enabled: p.enabled,
    };
    try {
      if (p.id) await update.mutateAsync({ id: p.id, body });
      else await create.mutateAsync(body);
      toast.ok(t("promocodes.saved"));
      setEdit(null);
    } catch (e) {
      toast.error(errorText(e));
      throw e;
    }
  };

  const toggleCode = async (p: PromoCode) => {
    try { await toggle.mutateAsync({ id: p.id, enabled: !p.enabled }); }
    catch (e) { toast.error(errorText(e)); }
  };

  const removeCode = async (p: PromoCode) => {
    if (!window.confirm(t("promocodes.deleteConfirm", { code: p.code }))) return;
    try { await remove.mutateAsync(p.id); }
    catch (e) { toast.error(errorText(e)); }
  };

  return (
    <>
      <PageHeader title={t("promocodes.title")} sub={t("promocodes.subtitle")} />
      <div className="mb-4 flex gap-2">
        <Button variant={tab === "codes" ? "primary" : "glass"} onClick={() => setTab("codes")}>{t("promocodes.codes")}</Button>
        <Button variant={tab === "history" ? "primary" : "glass"} onClick={() => setTab("history")}>{t("promocodes.history")}</Button>
        {tab === "codes" && <Button className="ml-auto" variant="primary" onClick={() => setEdit({ ...empty })}><Plus size={16} />{t("promocodes.create")}</Button>}
      </div>
      {tab === "codes" ? (
        <section className="card glass">
          {codes.isError && !codes.data ? <ErrorState text={errorText(codes.error)} onRetry={() => void codes.refetch()} /> : loading ? <p>{t("promocodes.loading")}</p> : items.length === 0 ? <p>{t("promocodes.empty")}</p> : (
            <ul className="row-list">
              {items.map((p) => (
                <li key={p.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 py-3">
                  <div>
                    <div className="flex flex-wrap items-center gap-2">
                      <b className="num">{p.code}</b>
                      <Pill tone={statusTone(p.status)}>{statusText(p.status)}</Pill>
                      <span className="text-xs">{valueText(p)}</span>
                    </div>
                    <div className="mt-1 text-xs text-[var(--ink-500)]">{p.name || p.description || "—"} · {t("promocodes.used")} {p.used_count}{p.max_uses ? ` / ${p.max_uses}` : ""}</div>
                  </div>
                  <div className="flex gap-1">
                    <Button size="sm" variant="ghost" onClick={() => void toggleCode(p)}><Power size={15} /></Button>
                    <Button size="sm" variant="ghost" onClick={() => setEdit(p)}>{t("promocodes.edit")}</Button>
                    <Button size="sm" variant="ghost" onClick={() => void removeCode(p)}><Trash2 size={15} /></Button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </section>
      ) : (
        <section className="card glass">
          <div className="mb-3 flex gap-2"><input className="input" placeholder={t("promocodes.historySearch")} value={search} onChange={(e) => setSearch(e.target.value)} /></div>
          {history.isError && !history.data ? <ErrorState text={errorText(history.error)} onRetry={() => void history.refetch()} /> : history.isLoading ? <p>{t("promocodes.loading")}</p> : filtered.length === 0 ? <p>{t("promocodes.noHistory")}</p> : (
            <ul className="row-list">
              {filtered.map((r) => <li key={r.id} className="py-3 text-sm"><b>{r.code}</b> · {t("promocodes.user")} #{r.user_id ?? t("promocodes.emptyValue")} · {t("promocodes.telegramId")} {r.tg_id} · {redemptionStatusText(r.status)}<div className="text-xs text-[var(--ink-500)]">{new Date(r.redeemed_at).toLocaleString()} · {r.days ? `${r.days} ${t("promocodes.daysUnit")} ` : ""}{r.bytes ? `${(r.bytes / 1073741824).toFixed(1)} ${t("promocodes.trafficUnit")} ` : ""}{r.discount_amount ? `−${r.discount_amount} ${r.currency}` : ""}</div></li>)}
            </ul>
          )}
        </section>
      )}
      {edit && <PromoEditor value={edit} onClose={() => setEdit(null)} onSave={save} />}
    </>
  );
}

function PromoEditor({ value, onClose, onSave }: { value: PromoCode; onClose: () => void; onSave: (p: Omit<PromoCode, "created_at" | "created_by">) => Promise<void> }) {
  const pools = usePools();
  const tariffs = useTariffs();
  const [p, setP] = useState<Draft>(() => ({ ...value, value: value.type === "traffic" ? value.value / 1073741824 : value.type === "fixed" ? fromUnits(value.value, value.currency) : value.value, min_order: fromUnits(value.min_order, value.currency), max_discount: fromUnits(value.max_discount, value.currency), starts_at: dateInput(value.starts_at), ends_at: dateInput(value.ends_at), max_uses: value.max_uses, tariff_ids: [...value.tariff_ids] }));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const set = (key: keyof Draft) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setP((x) => ({ ...x, [key]: e.target.type === "checkbox" ? (e.target as HTMLInputElement).checked : e.target.value }));
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    setSaving(true);
    try {
      await onSave({ ...p, value: p.type === "traffic" ? Math.round(Number(p.value) * 1073741824) : p.type === "fixed" ? toUnits(p.value, p.currency) : Number(p.value), per_user_limit: Number(p.per_user_limit), discount_ttl: Number(p.discount_ttl),
        // Sums mean nothing without a currency: a code for any currency has none.
        min_order: p.currency ? toUnits(p.min_order, p.currency) : 0, max_discount: p.currency ? toUnits(p.max_discount, p.currency) : 0, max_uses: p.max_uses ? Number(p.max_uses) : undefined, tariff_ids: p.tariff_ids, starts_at: p.starts_at || undefined, ends_at: p.ends_at || undefined });
    } catch (e) {
      setError(errorText(e));
    } finally {
      setSaving(false);
    }
  };
  return <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"><div className="card glass max-h-[90vh] w-full max-w-2xl overflow-auto"><div className="card-head"><div><h2 className="card-title">{p.id ? t("promocodes.edit") : t("promocodes.new")}</h2><div className="card-sub">{t("promocodes.createHint")}</div></div></div>{error && <div className="mb-3 rounded-xl border border-[var(--berry-600)]/30 bg-[var(--berry-50)] p-3 text-sm text-[var(--berry-600)]" role="alert">{error}</div>}<form className="grid gap-3" onSubmit={(e) => void submit(e)}>
    <label className="field"><span className="lbl">{t("promocodes.code")}</span><input className="input" value={p.code} onChange={set("code")} maxLength={64} /></label>
    <label className="field"><span className="lbl">{t("promocodes.name")}</span><input className="input" value={p.name} onChange={set("name")} /></label>
    <label className="field"><span className="lbl">{t("promocodes.description")}</span><input className="input" value={p.description} onChange={set("description")} /></label>
    <label className="field"><span className="lbl">{t("promocodes.type")}</span><select className="input" value={p.type} onChange={set("type")}><option value="days">{t("promocodes.days")}</option><option value="traffic">{t("promocodes.traffic")}</option><option value="percent">{t("promocodes.percent")}</option><option value="fixed">{t("promocodes.fixed")}</option></select></label>
    <label className="field"><span className="lbl">{p.type === "fixed" && p.currency ? `${t("promocodes.value")}, ${unitOf(p.currency)}` : t("promocodes.value")}</span><input className="input" type="number" min={p.type === "fixed" && p.currency === "RUB" ? "0.01" : "1"} step={p.type === "fixed" && p.currency === "RUB" ? "0.01" : "1"} max={p.type === "days" ? 36500 : undefined} value={p.value} onChange={set("value")} /></label>
    {(p.type === "fixed" || p.type === "percent") && <label className="field"><span className="lbl">{t("promocodes.currency")}</span><select className="input" value={p.currency} onChange={set("currency")}>{p.type === "percent" || !p.currency ? <option value="">{t("promocodes.anyCurrency")}</option> : null}<option value="RUB">RUB</option><option value="XTR">XTR</option></select>{p.type === "percent" && !p.currency ? <span className="hint">{t("promocodes.anyCurrencyHint")}</span> : null}</label>}
    <label className="field"><span className="lbl">{t("promocodes.startsAt")}</span><input className="input" type="datetime-local" value={p.starts_at} onChange={set("starts_at")} /></label>
    <label className="field"><span className="lbl">{t("promocodes.endsAt")}</span><input className="input" type="datetime-local" value={p.ends_at} onChange={set("ends_at")} /></label>
    <label className="field"><span className="lbl">{t("promocodes.maxUses")}</span><input className="input" type="number" min="1" placeholder={t("promocodes.noLimit")} value={p.max_uses ?? ""} onChange={set("max_uses")} /></label>
    <label className="field"><span className="lbl">{t("promocodes.perUserLimit")}</span><input className="input" type="number" min="1" value={p.per_user_limit} onChange={set("per_user_limit")} /></label>
    {(p.type === "percent" || p.type === "fixed") && <>{p.currency ? <><label className="field"><span className="lbl">{`${t("promocodes.minOrder")}, ${unitOf(p.currency)}`}</span><input className="input" type="number" min="0" step={p.currency === "RUB" ? "0.01" : "1"} value={p.min_order} onChange={set("min_order")} /><span className="hint">{t("promocodes.zeroNoLimit")}</span></label><label className="field"><span className="lbl">{`${t("promocodes.maxDiscount")}, ${unitOf(p.currency)}`}</span><input className="input" type="number" min="0" step={p.currency === "RUB" ? "0.01" : "1"} value={p.max_discount} onChange={set("max_discount")} /><span className="hint">{t("promocodes.zeroNoLimit")}</span></label></> : null}<label className="field"><span className="lbl">{t("promocodes.discountTtl")}</span><input className="input" type="number" min="0" max={30 * 24 * 60 * 60} value={p.discount_ttl} onChange={set("discount_ttl")} /><span className="hint">{t("promocodes.discountTtlHint")}</span></label></>}
    <div className="field"><span className="lbl">{t("promocodes.tariffs")}</span><span className="hint">{t("promocodes.allTariffsHint")}</span>{tariffs.isError ? <p className="text-sm text-[var(--berry-600)]" role="alert">{errorText(tariffs.error)}</p> : <div className="flex max-h-32 flex-col gap-2 overflow-auto rounded-xl border border-[var(--hairline)] p-3">{(tariffs.data ?? []).map((tariff) => <label key={tariff.id} className="flex items-center gap-2 text-sm"><input type="checkbox" checked={p.tariff_ids.includes(tariff.id)} onChange={(e) => setP((x) => ({ ...x, tariff_ids: e.target.checked ? [...x.tariff_ids, tariff.id] : x.tariff_ids.filter((id) => id !== tariff.id) }))} />{tariff.name}</label>)}</div>}</div>
    <label className="flex gap-2"><input type="checkbox" checked={p.first_purchase_only} onChange={set("first_purchase_only")} />{t("promocodes.firstPurchaseOnly")}</label>
    <label className="flex gap-2"><input type="checkbox" checked={p.new_users_only} onChange={set("new_users_only")} />{t("promocodes.newUsersOnly")}</label>
    {p.type === "traffic" && <label className="field"><span className="lbl">{t("promocodes.trafficPool")}</span>{pools.isError ? <p className="text-sm text-[var(--berry-600)]" role="alert">{errorText(pools.error)}</p> : <select className="input" value={p.pool_id ?? ""} onChange={(e) => setP((x) => ({ ...x, pool_id: e.target.value ? Number(e.target.value) : undefined }))}><option value="">{t("promocodes.mainTraffic")}</option>{(pools.data ?? []).map((pool) => <option key={pool.id} value={pool.id}>{pool.name}</option>)}</select>}</label>}
    <label className="flex gap-2"><input type="checkbox" checked={p.enabled} onChange={set("enabled")} />{t("promocodes.enabled")}</label>
    <div className="flex justify-end gap-2 pt-3"><Button variant="ghost" type="button" onClick={onClose}>{t("promocodes.cancel")}</Button><Button variant="primary" type="submit" loading={saving} disabled={tariffs.isError || (p.type === "traffic" && pools.isError)}>{t("promocodes.save")}</Button></div></form></div></div>;
}
