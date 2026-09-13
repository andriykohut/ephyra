import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { profileOverviewQuery } from "@/api/queries";
import type { ProfileGenre, ProfileOverview, ProfileTopItem, WatchRange } from "@/api/types";
import { ActivityStrip } from "@/components/ActivityStrip";
import { Avatar } from "@/components/Avatar";
import { Panel } from "@/components/Panel";
import { PlayList } from "@/components/PlayList";
import { RankGrid, type RankRow } from "@/components/RankGrid";
import { Skeleton } from "@/components/Skeleton";
import { StaleBanner } from "@/components/StaleBanner";
import { fmtHours, fmtInt } from "@/lib/format";

// `since` is a SQLite datetime string in server-local wall time (see
// CLAUDE.md's note on watch timestamps); slicing the literal year avoids
// re-zoning it through Date parsing.
function sinceYear(since: string): string {
  return /^\d{4}/.test(since) ? since.slice(0, 4) : "—";
}

function HeaderStat({ label, value }: { label: string; value: string }) {
  return (
    <div className="leading-tight">
      <div className="font-mono text-[16px] font-semibold tabular-nums text-ink">{value}</div>
      <div className="text-[10px] font-semibold uppercase tracking-[0.14em] text-muted">
        {label}
      </div>
    </div>
  );
}

function ProfileHeader({ overview }: { overview: ProfileOverview }) {
  return (
    <Panel className="flex flex-wrap items-center gap-5 p-4">
      <Avatar userId={overview.user.id} name={overview.user.name} />
      <div className="font-display text-[24px] font-semibold text-ink">
        {overview.user.name || "unknown"}
      </div>
      <div className="flex flex-wrap items-center gap-5">
        <HeaderStat label="plays" value={fmtInt(overview.plays)} />
        <HeaderStat label="watched" value={fmtHours(overview.watch_sec)} />
        <HeaderStat label="since" value={sinceYear(overview.since)} />
      </div>
      <div className="ml-auto">
        <ActivityStrip days={overview.activity} />
      </div>
    </Panel>
  );
}

function ChartPanel({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Panel className="p-4">
      <div className="mb-3 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">
        {title}
      </div>
      {children}
    </Panel>
  );
}

// items and genres carry the same rank/name/value shape as people but under
// different field names -- these map each onto RankRow so RankGrid never
// needs to know which source a row came from.
function itemToRankRow(i: ProfileTopItem): RankRow {
  return {
    person: i.name,
    kind: i.scope,
    item_id: i.item_id,
    jf_url: i.jf_url,
    watch_sec: i.watch_sec,
    plays: i.plays,
    watch_sec_played: i.watch_sec_played,
    plays_played: i.plays_played,
  };
}

// GenreDTO carries no *_played breakdown (that lives in agg_profile_taste,
// owned by aggregate.Profiles and out of scope here) -- both fields read the
// same base value, so the panel's "all plays" label is what actually tells
// the user the toggle doesn't reach this one.
function genreToRankRow(g: ProfileGenre): RankRow {
  return {
    person: g.key,
    kind: "genre",
    jf_url: g.jf_url,
    watch_sec: g.watch_sec,
    plays: g.plays,
    watch_sec_played: g.watch_sec,
    plays_played: g.plays,
  };
}

function ProfileCharts({
  overview,
  playedOnly,
}: {
  overview: ProfileOverview;
  playedOnly: boolean;
}) {
  const actors = overview.people.filter((p) => p.kind === "actor");
  const directors = overview.people.filter((p) => p.kind === "director");
  const seriesRows = overview.items.filter((i) => i.scope === "series").map(itemToRankRow);
  const itemRows = overview.items.filter((i) => i.scope !== "series").map(itemToRankRow);
  const genreRows = overview.genres.map(genreToRankRow);

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
      <ChartPanel title="actors · by hours">
        <RankGrid
          rows={actors}
          metric="watch_sec"
          playedOnly={playedOnly}
          color="cyan"
          empty="No credited actors in this range."
        />
      </ChartPanel>
      <ChartPanel title="actors · by breadth">
        <RankGrid
          rows={actors}
          metric="distinct_titles"
          playedOnly={playedOnly}
          color="violet"
          empty="No credited actors in this range."
        />
      </ChartPanel>
      <ChartPanel title="directors · by hours">
        <RankGrid
          rows={directors}
          metric="watch_sec"
          playedOnly={playedOnly}
          color="cyan"
          empty="No credited directors in this range."
        />
      </ChartPanel>
      <ChartPanel title="directors · by breadth">
        <RankGrid
          rows={directors}
          metric="distinct_titles"
          playedOnly={playedOnly}
          color="violet"
          empty="No credited directors in this range."
        />
      </ChartPanel>
      <ChartPanel title="series">
        <RankGrid
          rows={seriesRows}
          metric="watch_sec"
          playedOnly={playedOnly}
          empty="No series watched in this range."
        />
      </ChartPanel>
      <ChartPanel title="top items">
        <RankGrid
          rows={itemRows}
          metric="watch_sec"
          playedOnly={playedOnly}
          empty="Nothing watched in this range."
        />
      </ChartPanel>
      <ChartPanel title="genres · all plays">
        <RankGrid
          rows={genreRows}
          metric="watch_sec"
          playedOnly={playedOnly}
          empty="No genre data in this range."
        />
      </ChartPanel>
    </div>
  );
}

export function ProfileOverviewView({
  userID,
  range,
  library,
  playedOnly = true,
}: {
  userID: string;
  range: WatchRange;
  library: string;
  playedOnly?: boolean;
}) {
  const q = useQuery(profileOverviewQuery(userID, range, library));

  if (q.isPending) {
    return <Skeleton className="h-[92px] w-full" />;
  }
  if (q.isError) {
    return (
      <Panel className="p-5">
        <p className="text-[13px] text-muted">{(q.error as Error).message}</p>
      </Panel>
    );
  }

  const overview = q.data.data;

  return (
    <div className="flex flex-col gap-6">
      {q.data.meta.stale && <StaleBanner />}
      <ProfileHeader overview={overview} />
      <div className="grid grid-cols-1 gap-4 min-[820px]:grid-cols-[minmax(0,340px)_1fr]">
        <PlayList userId={userID} library={library} nowPlaying={overview.now_playing} />
        <ProfileCharts overview={overview} playedOnly={playedOnly} />
      </div>
    </div>
  );
}
