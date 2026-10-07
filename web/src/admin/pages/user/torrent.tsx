import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, errorText, unwrap, type User } from "../../../api/client";
import { qk, useTorrent, useTorrentHits } from "../../../api/hooks";
import { Switch } from "../../../components/switch";
import { useToast } from "../../../components/toast";
import { Button } from "../../../components/ui";
import { t } from "../../../i18n";
import { dateShort, time } from "../../../lib/format";
import { HitRow, useSaveTorrent } from "../settings/torrent";
import { Section } from "./section";

// The user's side of the torrent blocker: a ban running, the check switched off, the
// latest catches. Shown while the blocker is on.
export function TorrentSection({ u }: { u: User }) {
  const cfg = useTorrent();
  const hits = useTorrentHits(u.id, 5);
  const save = useSaveTorrent();
  const qc = useQueryClient();
  const toast = useToast();
  const lift = useMutation({
    mutationFn: () => unwrap(api.POST("/api/v1/users/{id}/torrent-ban/lift", { params: { path: { id: u.id } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.users });
      void qc.invalidateQueries({ queryKey: qk.torrent });
      toast.ok(t("userDrawer.torrent.lifted"));
    },
    onError: (e) => toast.error(errorText(e)),
  });
  const c = cfg.data;
  if (!c?.enabled) return null;
  const exempt = c.exempt.some((x) => x.id === u.id);
  const setExempt = (on: boolean) => {
    const ids = c.exempt.map((x) => x.id).filter((id) => id !== u.id);
    save.mutate({ exempt: on ? [...ids, u.id] : ids });
  };
  const recent = hits.data?.pages[0] ?? [];
  return (
    <Section title={t("userDrawer.torrent.title")}>
      {u.torrent_ban ? (
        <div className="panel-soft mb-3 flex flex-wrap items-center justify-between gap-3 p-3">
          <span className="text-[13px] font-medium text-[var(--berry-600)]">
            {t("userDrawer.torrent.banned", { date: `${dateShort(u.torrent_ban)} ${time(u.torrent_ban)}` })}
          </span>
          <Button size="sm" loading={lift.isPending} onClick={() => lift.mutate()}>
            {t("userDrawer.torrent.lift")}
          </Button>
        </div>
      ) : null}
      <div className="flex items-start justify-between gap-3 py-1">
        <div>
          <div className="text-[13px] font-medium">{t("userDrawer.torrent.exempt")}</div>
          <div className="text-xs text-[var(--ink-500)]">{t("userDrawer.torrent.exemptHint")}</div>
        </div>
        <Switch checked={exempt} label={t("userDrawer.torrent.exempt")} disabled={save.isPending} onChange={setExempt} />
      </div>
      {recent.length ? (
        <>
          <div className="mt-3 text-xs font-medium text-[var(--ink-600)]">{t("userDrawer.torrent.recent")}</div>
          <ul className="row-list">
            {recent.map((h) => (
              <HitRow key={h.id} h={h} />
            ))}
          </ul>
        </>
      ) : hits.data ? (
        <p className="mt-2 text-xs text-[var(--ink-500)]">{t("userDrawer.torrent.none")}</p>
      ) : null}
    </Section>
  );
}
