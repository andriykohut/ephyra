import { useQuery } from "@tanstack/react-query";
import { createRoute } from "@tanstack/react-router";
import { useEffect } from "react";
import { profileListQuery, profileQuery } from "@/api/queries";
import type { Profile as P, WatchRange } from "@/api/types";
import { type Column, DataTable } from "@/components/DataTable";
import { Panel } from "@/components/Panel";
import { SegmentedControl } from "@/components/SegmentedControl";
import { Skeleton } from "@/components/Skeleton";
import { StaleBanner } from "@/components/StaleBanner";
import { StatCard } from "@/components/StatCard";
import { fmtInt, fmtRuntime } from "@/lib/format";
import { Route as rootRoute } from "./__root";

const RANGES: { value: WatchRange; label: string }[] = [
  { value: "30d", label: "30 days" },
  { value: "90d", label: "90 days" },
  { value: "1y", label: "1 year" },
  { value: "all", label: "All" },
];

const validateSearch = (s: Record<string, unknown>): { range: WatchRange; user: string } => ({
  range: (["30d", "90d", "1y", "all"].includes(s.range as string) ? s.range : "30d") as WatchRange,
  user: typeof s.user === "string" ? s.user : "",
});

const pct = (x: number) => `${Math.round(x * 100)}%`;

// finished / partial / bailed / unknown, cyan -> amber -> muted
const BUCKETS: { key: string; label: string; color: string }[] = [
  { key: "finished", label: "finished", color: "#4fe0d8" },
  { key: "partial", label: "partial", color: "#7ba7e6" },
  { key: "bailed", label: "bailed", color: "#ffc24b" },
  { key: "unknown", label: "no runtime", color: "#4c4a86" },
];

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Panel className="p-4">
      <div className="mb-3 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">
        {title}
      </div>
      {children}
    </Panel>
  );
}

function CompletionBar({ completion }: { completion: P["completion"] }) {
  const totals = new Map<string, number>();
  for (const c of completion) totals.set(c.bucket, (totals.get(c.bucket) ?? 0) + c.count);
  const sum = [...totals.values()].reduce((a, b) => a + b, 0);
  if (sum === 0)
    return <p className="font-mono text-[12px] text-muted">No verdicts in this range.</p>;

  return (
    <div>
      <div className="flex h-[46px] overflow-hidden rounded-[12px] border border-line/70">
        {BUCKETS.map((b) => {
          const n = totals.get(b.key) ?? 0;
          if (n === 0) return null;
          return (
            <div
              key={b.key}
              title={`${b.label} · ${fmtInt(n)}`}
              style={{ flex: `${n} 0 0`, background: b.color }}
              className="min-w-[3px] border-l border-black/25 first:border-l-0"
            />
          );
        })}
      </div>
      <div className="mt-2 flex flex-wrap gap-x-5 gap-y-1 font-mono text-[11.5px] text-muted">
        {BUCKETS.map((b) => {
          const n = totals.get(b.key) ?? 0;
          if (n === 0) return null;
          return (
            <span key={b.key}>
              <span className="font-medium tabular-nums text-ink">{fmtInt(n)}</span> {b.label}
            </span>
          );
        })}
      </div>
    </div>
  );
}

function ShareBars({
  user,
  baseline,
  label = "data",
}: {
  user: { key: string; watch_sec: number }[];
  baseline: { key: string; watch_sec: number }[];
  label?: string;
}) {
  const uTotal = user.reduce((a, g) => a + g.watch_sec, 0) || 1;
  const bTotal = baseline.reduce((a, g) => a + g.watch_sec, 0) || 1;
  const bShare = new Map(baseline.map((g) => [g.key, g.watch_sec / bTotal]));
  const rows = [...user].sort((a, b) => b.watch_sec - a.watch_sec).slice(0, 8);
  if (rows.length === 0)
    return <p className="font-mono text-[12px] text-muted">No {label} data for this user.</p>;

  return (
    <div className="flex flex-col gap-2">
      {rows.map((g) => {
        const you = g.watch_sec / uTotal;
        const lib = bShare.get(g.key) ?? 0;
        return (
          <div key={g.key} className="grid grid-cols-[90px_1fr] items-center gap-3">
            <span className="truncate font-mono text-[12px] text-ink" title={g.key}>
              {g.key}
            </span>
            <span className="relative block h-[14px] rounded bg-panel/60">
              <span
                className="absolute inset-y-0 left-0 rounded bg-cyan/70"
                style={{ width: `${Math.min(100, you * 100)}%` }}
              />
              <span
                className="absolute inset-y-0 left-0 border-r-2 border-violet/80"
                style={{ width: `${Math.min(100, lib * 100)}%` }}
                title={`library ${pct(lib)}`}
              />
            </span>
          </div>
        );
      })}
      <p className="mt-1 font-mono text-[11px] text-muted">
        bar = this user's share · marker = library share
      </p>
    </div>
  );
}

export function ProfileView({
  user,
  range,
  onUser,
  onRange,
}: {
  user: string;
  range: WatchRange;
  onUser: (v: string) => void;
  onRange: (v: WatchRange) => void;
}) {
  const list = useQuery(profileListQuery());
  const prof = useQuery(profileQuery(user, range));

  const users = list.data?.data.users ?? [];
  useEffect(() => {
    if (user === "" && users.length > 0) onUser(users[0].id);
  }, [user, users, onUser]);

  const controls = (
    <div className="flex flex-wrap items-center gap-3">
      <SegmentedControl ariaLabel="range" value={range} onChange={onRange} options={RANGES} />
      <select
        aria-label="user"
        value={user}
        onChange={(e) => onUser(e.target.value)}
        className="rounded-[10px] border border-line bg-panel/60 px-3 py-1.5 text-[12.5px] text-ink"
      >
        {user === "" && <option value="">Pick a user…</option>}
        {users.map((u) => (
          <option key={u.id} value={u.id}>
            {u.name}
          </option>
        ))}
      </select>
    </div>
  );

  const heading = (
    <h1 className="font-display text-[clamp(24px,4vw,32px)] font-bold tracking-[-0.03em] text-ink">
      Profiles
    </h1>
  );

  if (list.isPending) {
    return (
      <div className="flex flex-col gap-6">
        {heading}
        {controls}
        <Skeleton className="h-[420px] w-full" />
      </div>
    );
  }
  if (list.isError) {
    return (
      <Panel className="p-5">
        <p className="text-[13px] text-muted">{(list.error as Error).message}</p>
      </Panel>
    );
  }

  const pluginAbsent = !list.data.data.plugin_available;

  return (
    <div className="flex flex-col gap-6">
      {heading}
      {controls}
      {prof.data?.meta.stale && <StaleBanner />}

      {pluginAbsent ? (
        <Panel className="p-5">
          <div className="font-display text-lg font-semibold text-ink">
            Playback Reporting not detected
          </div>
          <p className="mt-1 text-[13px] leading-relaxed text-muted">
            Profiles need the Playback Reporting plugin. In Jellyfin: Dashboard → Plugins → Catalog
            → Playback Reporting → install, then restart Jellyfin. Ephyra picks it up on the next
            refresh.
          </p>
        </Panel>
      ) : user === "" ? (
        <Panel className="p-5">
          <p className="text-[13px] text-muted">Pick a user to see their profile.</p>
        </Panel>
      ) : prof.isPending || !prof.data ? (
        <Skeleton className="h-[420px] w-full" />
      ) : prof.isError ? (
        <Panel className="p-5">
          <p className="text-[13px] text-muted">{(prof.error as Error).message}</p>
        </Panel>
      ) : (
        <ProfileBody d={prof.data.data} onRange={onRange} />
      )}
    </div>
  );
}

const abandonedCols: Column<P["abandoned"][number]>[] = [
  { key: "name", header: "title", cell: (r) => r.name || r.series_name },
  { key: "bailed", header: "bailed", align: "right", cell: (r) => fmtInt(r.bailed_count) },
];
const rewatchCols: Column<P["rewatch"][number]>[] = [
  { key: "name", header: "title", cell: (r) => r.name || r.series_name },
  { key: "days", header: "watch days", align: "right", cell: (r) => fmtInt(r.watch_days) },
];
const bingeCols: Column<P["binge"][number]>[] = [
  { key: "series", header: "series", cell: (r) => r.series_name },
  { key: "eps", header: "episodes", align: "right", cell: (r) => fmtInt(r.run_episodes) },
  { key: "start", header: "started", align: "right", cell: (r) => r.run_start },
];

function ProfileBody({ d, onRange }: { d: P; onRange: (v: WatchRange) => void }) {
  const empty = d.summary.plays === 0;
  if (empty) {
    return (
      <Panel className="p-5">
        <div className="font-display text-lg font-semibold text-ink">No plays in this range</div>
        <p className="mt-1 text-[13px] text-muted">
          Coverage runs {d.summary.first_play || "—"} to {d.summary.last_play || "—"}.
        </p>
        <button
          type="button"
          onClick={() => onRange("all")}
          className="mt-3 rounded-[10px] border border-line px-3 py-1.5 text-[13px] text-ink hover:border-cyan hover:text-cyan"
        >
          View all
        </button>
      </Panel>
    );
  }

  return (
    <>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatCard label="watch time" value={fmtRuntime(d.summary.watch_sec)} />
        <StatCard label="finished" value={pct(d.summary.finished_pct)} />
        <StatCard label="rewatch rate" value={pct(d.summary.rewatch_pct)} />
        <StatCard
          label="longest binge"
          value={
            d.summary.longest_binge.episodes > 0 ? `${d.summary.longest_binge.episodes} eps` : "—"
          }
          mote
        />
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <Section title="completion vs abandonment">
          <CompletionBar completion={d.completion} />
          <div className="mt-3">
            <DataTable
              columns={abandonedCols}
              rows={d.abandoned}
              getKey={(r) => `${r.scope}:${r.item_id}`}
              empty="Nothing bailed on — impressive."
            />
          </div>
        </Section>

        <Section title="rewatch vs first-watch">
          <div className="mb-3 font-mono text-[26px] font-semibold tabular-nums text-ink">
            {pct(d.summary.rewatch_pct)}
            <span className="ml-2 text-[12px] font-normal text-muted">
              of watch days are rewatches
            </span>
          </div>
          <DataTable
            columns={rewatchCols}
            rows={d.rewatch}
            getKey={(r) => `${r.scope}:${r.item_id}`}
            empty="No rewatches yet."
          />
        </Section>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <Section title="binge / marathon runs">
          <div className="mb-3">
            <StatCard
              label={d.summary.longest_binge.series_name || "longest run"}
              value={
                d.summary.longest_binge.episodes > 0
                  ? `${d.summary.longest_binge.episodes} in a row`
                  : "—"
              }
            />
          </div>
          <DataTable
            columns={bingeCols}
            rows={d.binge}
            getKey={(r) => `${r.series_id}:${r.run_start}`}
            empty="No runs of 2+ episodes."
          />
          {d.summary.show_of_range.series_name && (
            <p className="mt-2 font-mono text-[11.5px] text-muted">
              show of the range:{" "}
              <span className="text-ink">{d.summary.show_of_range.series_name}</span>
            </p>
          )}
        </Section>

        <Section title="taste fingerprint">
          <ShareBars user={d.taste.genre} baseline={d.baseline.genre} label="genre" />
          {d.taste.signature_genres.length > 0 && (
            <p className="mt-3 font-mono text-[11.5px] text-muted">
              leans <span className="text-ink">{d.taste.signature_genres.join(", ")}</span>
            </p>
          )}

          <div className="mt-4 mb-2 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">
            tags
          </div>
          <ShareBars user={d.taste.tag} baseline={d.baseline.tag} label="tag" />
          {d.taste.signature_tags.length > 0 && (
            <p className="mt-3 font-mono text-[11.5px] text-muted">
              also leans <span className="text-ink">{d.taste.signature_tags.join(", ")}</span>
            </p>
          )}

          {d.tag_overlap.length > 0 && (
            <div className="mt-4">
              <div className="mb-2 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">
                overlap with others
              </div>
              <div className="flex flex-col gap-1.5">
                {d.tag_overlap.map((o) => (
                  <div key={o.user} className="grid grid-cols-[120px_1fr] items-center gap-3">
                    <span className="truncate font-mono text-[12px] text-ink" title={o.user_name}>
                      {o.user_name}{" "}
                      <span className="text-muted">{Math.round(o.cosine * 100)}%</span>
                    </span>
                    <span className="relative block h-[14px] rounded bg-panel/60">
                      <span
                        className="absolute inset-y-0 left-0 rounded bg-violet/70"
                        style={{ width: `${Math.min(100, o.cosine * 100)}%` }}
                        title={o.shared.join(", ")}
                      />
                    </span>
                  </div>
                ))}
              </div>
              <p className="mt-1 font-mono text-[11px] text-muted">
                bar = tag-taste similarity · hover for shared tags
              </p>
            </div>
          )}

          <div className="mt-4 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[11.5px] text-muted">
            {[...d.taste.decade]
              .sort((a, b) => a.key.localeCompare(b.key))
              .map((g) => (
                <span key={g.key}>
                  <span className="tabular-nums text-ink">{g.key}</span> {fmtRuntime(g.watch_sec)}
                </span>
              ))}
          </div>
        </Section>
      </div>
    </>
  );
}

function ProfilePage() {
  const { range, user } = Route.useSearch();
  const nav = Route.useNavigate();
  return (
    <ProfileView
      range={range}
      user={user}
      onRange={(v) => nav({ search: (s) => ({ ...s, range: v }) })}
      onUser={(v) => nav({ search: (s) => ({ ...s, user: v }) })}
    />
  );
}

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/profile",
  validateSearch,
  component: ProfilePage,
});
