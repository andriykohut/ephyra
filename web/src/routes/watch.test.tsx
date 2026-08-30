import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { Envelope, WatchStats as WS } from "@/api/types";
import { WatchStatsView } from "./watch";

vi.mock("@/charts/EChart", () => ({ EChart: () => <div data-testid="echart" /> }));

const base: WS = {
  plugin_available: true,
  range: "30d",
  user: "all",
  users: [
    { id: "u1", name: "alice" },
    { id: "u2", name: "bob" },
  ],
  coverage: { first_play: "2025-01-01", last_play: "2025-01-31", total_plays: 42 },
  totals: {
    watch_seconds: 36000,
    plays: 20,
    active_users: 2,
    direct_play_pct: 0.8,
    video_transcode_pct: 0.1,
  },
  top_movies: [{ item_id: "m1", name: "Bravo", plays: 5, watch_sec: 9000 }],
  top_series: [{ series_id: "s1", name: "Some Show", plays: 8, watch_sec: 12000 }],
  top_episodes: [
    { item_id: "e1", name: "Pilot", series_name: "Some Show", plays: 2, watch_sec: 2400 },
  ],
  active_users: [{ user_id: "u1", name: "alice", watch_sec: 30000, plays: 15, distinct_titles: 6 }],
  trend: [{ day: "2025-01-06", watch_sec: 9000, plays: 3 }],
  heatmap: [{ dow: 1, hour: 20, watch_sec: 9000, plays: 3 }],
  play_method_weekly: [
    {
      week: "2025-01-06",
      DirectPlay: 10,
      Remux: 1,
      AudioTranscode: 2,
      VideoTranscode: 3,
      Other: 0,
    },
  ],
  most_played_core: [
    {
      scope: "movie",
      item_id: "m9",
      name: "Fav Movie",
      play_count: 12,
      last_played_at: "2025-01-20T00:00:00Z",
    },
  ],
};

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}
const noop = () => {};

function mock(data: WS) {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(
      JSON.stringify({ data, meta: { generated_at: "x", stale: false } } satisfies Envelope<WS>),
      { status: 200 },
    ),
  );
}

afterEach(() => vi.restoreAllMocks());

test("renders panels from data", async () => {
  mock(base);
  render(wrap(<WatchStatsView range="30d" user="all" onRange={noop} onUser={noop} />));
  expect(await screen.findByText("Some Show")).toBeInTheDocument();
  expect(screen.getByText("Fav Movie")).toBeInTheDocument();
  expect(screen.getByText(/Jellyfin's own play counters/i)).toBeInTheDocument();
  expect((await screen.findAllByTestId("echart")).length).toBeGreaterThanOrEqual(2);
});

test("plugin-absent shows install card but keeps users + core", async () => {
  mock({
    ...base,
    plugin_available: false,
    top_movies: [],
    top_series: [],
    top_episodes: [],
    trend: [],
    heatmap: [],
    play_method_weekly: [],
    active_users: [],
    totals: { ...base.totals, plays: 0 },
  });
  render(wrap(<WatchStatsView range="30d" user="all" onRange={noop} onUser={noop} />));
  expect(await screen.findByText("Playback Reporting not detected")).toBeInTheDocument();
  expect(screen.getByText("Fav Movie")).toBeInTheDocument();
});

test("empty range shows coverage-aware message", async () => {
  mock({
    ...base,
    top_movies: [],
    top_series: [],
    top_episodes: [],
    trend: [],
    heatmap: [],
    play_method_weekly: [],
    active_users: [],
    totals: { ...base.totals, plays: 0, watch_seconds: 0 },
  });
  render(wrap(<WatchStatsView range="30d" user="all" onRange={noop} onUser={noop} />));
  await waitFor(() => expect(screen.getByText(/no plays in this range/i)).toBeInTheDocument());
  expect(screen.getByText(/2025-01-01/)).toBeInTheDocument();
});
