import { useQuery } from "@tanstack/react-query";
import { createRoute, Link, redirect } from "@tanstack/react-router";
import { useState } from "react";
import { librariesQuery, profileListQuery } from "@/api/queries";
import type { WatchRange } from "@/api/types";
import { LibrarySelect } from "@/components/LibrarySelect";
import { Panel } from "@/components/Panel";
import { SegmentedControl } from "@/components/SegmentedControl";
import { Skeleton } from "@/components/Skeleton";
import { cn } from "@/lib/utils";
import { Route as rootRoute } from "./__root";
import { ProfileOverviewView } from "./profile.overview";
import { ProfileStatsView } from "./profile.stats";

type Tab = "overview" | "stats";

const RANGES: { value: WatchRange; label: string }[] = [
  { value: "30d", label: "30 days" },
  { value: "90d", label: "90 days" },
  { value: "1y", label: "1 year" },
  { value: "all", label: "All" },
];

const TABS: { value: Tab; label: string }[] = [
  { value: "overview", label: "Overview" },
  { value: "stats", label: "Stats" },
];

interface ShellSearch {
  range: WatchRange;
  tab: Tab;
  library: string;
}

const validateSearch = (s: Record<string, unknown>): ShellSearch => ({
  range: (["30d", "90d", "1y", "all"].includes(s.range as string) ? s.range : "30d") as WatchRange,
  tab: s.tab === "stats" ? "stats" : "overview",
  library: typeof s.library === "string" ? s.library : "all",
});

// Pushed to the right of the chrome row; defaults on since it changes no
// request -- both the plain and *_played metrics are already in the payload,
// this only picks which fields the charts read.
function FinishedOnlyPill({ value, onChange }: { value: boolean; onChange: () => void }) {
  return (
    <button
      type="button"
      aria-pressed={value}
      onClick={onChange}
      className={cn(
        "ml-auto rounded-full border px-3 py-1.5 text-[12.5px] font-medium transition-colors",
        value ? "border-cyan/40 bg-cyan/15 text-cyan" : "border-line text-muted hover:text-ink",
      )}
    >
      Finished only
    </button>
  );
}

function ProfileShellPage() {
  const { user } = Route.useParams();
  const { range, tab, library } = Route.useSearch();
  const nav = Route.useNavigate();
  const libs = useQuery(librariesQuery());
  const [playedOnly, setPlayedOnly] = useState(true);

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center gap-3">
        <div
          role="tablist"
          aria-label="profile section"
          className="inline-flex rounded-[10px] border border-line bg-panel/60 p-0.5"
        >
          {TABS.map((t) => (
            <button
              key={t.value}
              type="button"
              role="tab"
              aria-selected={tab === t.value}
              onClick={() => nav({ search: (s) => ({ ...s, tab: t.value }) })}
              className={`rounded-[8px] px-3 py-1.5 text-[12.5px] font-medium transition-colors ${
                tab === t.value ? "bg-cyan/15 text-cyan" : "text-muted hover:text-ink"
              }`}
            >
              {t.label}
            </button>
          ))}
        </div>
        <LibrarySelect
          value={library}
          libraries={libs.data?.data ?? []}
          onChange={(v) => nav({ search: (s) => ({ ...s, library: v }) })}
        />
        <SegmentedControl
          ariaLabel="range"
          value={range}
          onChange={(v) => nav({ search: (s) => ({ ...s, range: v }) })}
          options={RANGES}
        />
        {tab === "overview" && (
          <FinishedOnlyPill value={playedOnly} onChange={() => setPlayedOnly((v) => !v)} />
        )}
      </div>

      {tab === "overview" ? (
        <ProfileOverviewView
          userID={user}
          range={range}
          library={library}
          playedOnly={playedOnly}
        />
      ) : (
        <ProfileStatsView
          user={user}
          range={range}
          library={library}
          onRange={(v) => nav({ search: (s) => ({ ...s, range: v }) })}
          onUser={(v) => nav({ to: "/profile/$user", params: { user: v }, search: (s) => s })}
        />
      )}
    </div>
  );
}

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/profile/$user",
  validateSearch,
  component: ProfileShellPage,
});

// --- legacy "/profile?user=…" URLs redirect to the path form ---

interface LegacySearch {
  user: string;
  range: WatchRange;
}

const validateLegacySearch = (s: Record<string, unknown>): LegacySearch => ({
  user: typeof s.user === "string" ? s.user : "",
  range: (["30d", "90d", "1y", "all"].includes(s.range as string) ? s.range : "30d") as WatchRange,
});

function ProfileLegacyPage() {
  const list = useQuery(profileListQuery());

  if (list.isPending) {
    return <Skeleton className="h-[240px] w-full" />;
  }
  if (list.isError) {
    return (
      <Panel className="p-5">
        <p className="text-[13px] text-muted">{(list.error as Error).message}</p>
      </Panel>
    );
  }
  if (!list.data.data.plugin_available) {
    return (
      <Panel className="p-5">
        <div className="font-display text-lg font-semibold text-ink">
          Playback Reporting not detected
        </div>
        <p className="mt-1 text-[13px] leading-relaxed text-muted">
          Profiles need the Playback Reporting plugin. In Jellyfin: Dashboard → Plugins → Catalog →
          Playback Reporting → install, then restart Jellyfin. Ephyra picks it up on the next
          refresh.
        </p>
      </Panel>
    );
  }

  const users = list.data.data.users;
  if (users.length === 0) {
    return (
      <Panel className="p-5">
        <p className="text-[13px] text-muted">No users with playback history yet.</p>
      </Panel>
    );
  }

  return (
    <Panel className="p-5">
      <p className="mb-3 text-[13px] text-muted">Pick a user to see their profile.</p>
      <div className="flex flex-col gap-1">
        {users.map((u) => (
          <Link
            key={u.id}
            to="/profile/$user"
            params={{ user: u.id }}
            search={{ range: "30d", tab: "overview", library: "all" }}
            className="rounded-[10px] px-3 py-2 text-[13px] text-ink hover:bg-panel/60 hover:text-cyan"
          >
            {u.name}
          </Link>
        ))}
      </div>
    </Panel>
  );
}

export const LegacyRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/profile",
  validateSearch: validateLegacySearch,
  beforeLoad: ({ search }) => {
    if (search.user) {
      throw redirect({
        to: "/profile/$user",
        params: { user: search.user },
        search: { range: search.range, tab: "overview", library: "all" },
      });
    }
  },
  component: ProfileLegacyPage,
});
