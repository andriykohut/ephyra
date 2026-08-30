import { useQuery } from "@tanstack/react-query";
import { createRoute } from "@tanstack/react-router";
import { cleanupQuery } from "@/api/queries";
import type { CleanupItem } from "@/api/types";
import { type Column, DataTable } from "@/components/DataTable";
import { Panel } from "@/components/Panel";
import { SegmentedControl } from "@/components/SegmentedControl";
import { Skeleton } from "@/components/Skeleton";
import { StaleBanner } from "@/components/StaleBanner";
import { fmtBytes } from "@/lib/format";
import { Route as rootRoute } from "./__root";

type Mode = "never" | "stale";
type Sort = "size" | "added";

const validateSearch = (s: Record<string, unknown>): { mode: Mode; sort: Sort } => ({
  mode: s.mode === "stale" ? "stale" : "never",
  sort: s.sort === "added" ? "added" : "size",
});

function fmtDate(iso: string | null): string {
  return iso ? iso.slice(0, 10) : "—";
}

const columns: Column<CleanupItem>[] = [
  {
    key: "name",
    header: "title",
    cell: (r) => (
      <span>
        <span className="text-muted">{r.scope === "series" ? "▤ " : "▸ "}</span>
        {r.name}
        {r.scope === "series" && <span className="text-muted"> · {r.episodes} eps</span>}
      </span>
    ),
  },
  {
    key: "library",
    header: "library",
    cell: (r) => <span className="text-muted">{r.library}</span>,
  },
  { key: "bytes", header: "size", align: "right", cell: (r) => fmtBytes(r.bytes) },
  {
    key: "added",
    header: "added",
    align: "right",
    cell: (r) => <span className="text-muted">{fmtDate(r.added_at)}</span>,
  },
  {
    key: "last",
    header: "last played",
    align: "right",
    cell: (r) => <span className="text-muted">{fmtDate(r.last_played_at)}</span>,
  },
];

export function CleanupView({
  mode,
  sort,
  onMode,
  onSort,
}: {
  mode: Mode;
  sort: Sort;
  onMode: (v: Mode) => void;
  onSort: (v: Sort) => void;
}) {
  const q = useQuery(cleanupQuery(mode, sort));

  return (
    <div className="flex flex-col gap-6">
      <h1 className="font-display text-[clamp(24px,4vw,32px)] font-bold tracking-[-0.03em] text-ink">
        Cleanup
      </h1>

      <Panel className="p-5">
        <div className="font-mono text-[28px] font-semibold tabular-nums text-cyan">
          {q.data ? fmtBytes(q.data.data.reclaimable_bytes) : "—"}
        </div>
        <p className="mt-1 text-[12.5px] text-muted">
          reclaimable · Ephyra never deletes anything — this is a shortlist you act on in Jellyfin
        </p>
      </Panel>

      <div className="flex flex-wrap items-center gap-3">
        <SegmentedControl
          ariaLabel="watched filter"
          value={mode}
          onChange={onMode}
          options={[
            { value: "never", label: "Never watched" },
            { value: "stale", label: "Watched, then untouched 1y+" },
          ]}
        />
        <SegmentedControl
          ariaLabel="sort"
          value={sort}
          onChange={onSort}
          options={[
            { value: "size", label: "Biggest" },
            { value: "added", label: "Longest resident" },
          ]}
        />
        <a
          className="rounded-[10px] border border-line px-3 py-1.5 text-[12.5px] text-ink hover:border-cyan hover:text-cyan"
          href={`/api/cleanup?mode=${mode}&sort=${sort}&format=csv`}
        >
          Export CSV
        </a>
      </div>

      {q.data?.meta.stale && <StaleBanner />}

      {q.isPending ? (
        <Skeleton className="h-[320px] w-full" />
      ) : q.isError ? (
        <Panel className="p-5">
          <p className="text-[13px] text-muted">{(q.error as Error).message}</p>
          <button
            type="button"
            onClick={() => q.refetch()}
            className="mt-3 rounded-[10px] border border-line px-3 py-1.5 text-[13px] text-ink hover:border-cyan hover:text-cyan"
          >
            Try again
          </button>
        </Panel>
      ) : (
        <Panel className="p-1.5">
          <DataTable
            columns={columns}
            rows={q.data.data.items}
            getKey={(r) => r.item_id}
            empty={
              mode === "never"
                ? "Nothing matches — it's all been watched."
                : "Nothing's gone stale."
            }
          />
          {q.data.data.truncated && (
            <div className="px-3 py-2 text-[12px] text-muted">
              Showing {q.data.data.items.length} of {q.data.data.match_count} — export CSV for the
              full list.
            </div>
          )}
        </Panel>
      )}
    </div>
  );
}

function CleanupPage() {
  const { mode, sort } = Route.useSearch();
  const nav = Route.useNavigate();
  return (
    <CleanupView
      mode={mode}
      sort={sort}
      onMode={(v) => nav({ search: (s) => ({ ...s, mode: v }) })}
      onSort={(v) => nav({ search: (s) => ({ ...s, sort: v }) })}
    />
  );
}

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/cleanup",
  validateSearch,
  component: CleanupPage,
});
