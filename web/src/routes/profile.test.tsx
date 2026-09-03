import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { Envelope, Profile, ProfileList } from "@/api/types";
import { ProfileView } from "./profile";

const list: Envelope<ProfileList> = {
  data: {
    plugin_available: true,
    coverage: { first_play: "2025-01-01", last_play: "2025-05-01", total_plays: 120 },
    users: [
      {
        id: "u1",
        name: "alice",
        total_watch_sec: 9000,
        total_plays: 12,
        finished_pct: 0.6,
        rewatch_pct: 0.25,
        longest_binge_episodes: 6,
        last_play: "2025-05-01",
      },
    ],
  },
  meta: { generated_at: new Date().toISOString(), stale: false },
};

const detail: Envelope<Profile> = {
  data: {
    range: "30d",
    user: { id: "u1", name: "alice" },
    summary: {
      watch_sec: 9000,
      plays: 12,
      distinct_titles: 5,
      days_active: 7,
      finished_pct: 0.6,
      bailed_pct: 0.1,
      rewatch_pct: 0.25,
      longest_binge: { episodes: 6, series_name: "The Show" },
      show_of_range: { series_id: "s1", series_name: "The Show" },
      first_play: "2025-01-01",
      last_play: "2025-05-01",
    },
    completion: [
      { scope: "movie", bucket: "finished", count: 3 },
      { scope: "movie", bucket: "bailed", count: 1 },
    ],
    abandoned: [
      { scope: "movie", item_id: "m9", name: "Half-Watched", series_name: "", bailed_count: 2 },
    ],
    rewatch: [{ scope: "movie", item_id: "m1", name: "Alpha", series_name: "", watch_days: 3 }],
    binge: [
      {
        series_id: "s1",
        series_name: "The Show",
        run_episodes: 6,
        run_start: "2025-02-01",
        run_end: "2025-02-01",
      },
    ],
    taste: {
      genre: [{ key: "Drama", watch_sec: 6000, plays: 4 }],
      decade: [{ key: "1990", watch_sec: 6000, plays: 4 }],
      length: [{ key: "90-120m", watch_sec: 6000, plays: 4 }],
      tag: [{ key: "heist", watch_sec: 6000, plays: 4 }],
      signature_genres: ["Drama"],
      signature_tags: ["heist"],
    },
    baseline: {
      genre: [{ key: "Drama", watch_sec: 1000 }],
      decade: [{ key: "1990", watch_sec: 1000 }],
      length: [{ key: "90-120m", watch_sec: 1000 }],
      tag: [{ key: "heist", watch_sec: 1000 }],
    },
    tag_overlap: [{ user: "u2", user_name: "bob", cosine: 0.62, shared: ["heist", "vault"] }],
  },
  meta: { generated_at: new Date().toISOString(), stale: false },
};

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}
const noop = () => {};
afterEach(() => vi.restoreAllMocks());

test("renders the picked user's panels", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const url = String(input);
    const body = url.includes("/api/profile/") ? detail : list;
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });

  render(wrap(<ProfileView user="u1" range="30d" onUser={noop} onRange={noop} />));

  await waitFor(() => expect(screen.getByText(/Alpha/)).toBeInTheDocument());
  expect(screen.getAllByText(/The Show/).length).toBeGreaterThan(0);
  expect(screen.getByText(/Half-Watched/)).toBeInTheDocument();
  expect(screen.getAllByText(/Drama/).length).toBeGreaterThan(0);
  expect(screen.getAllByText("heist").length).toBeGreaterThan(0);
  expect(screen.getByText(/bob/)).toBeInTheDocument();
  expect(screen.getByText(/62%/)).toBeInTheDocument();
});

test("hides the overlap block when tag_overlap is empty", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const url = String(input);
    const body = url.includes("/api/profile/")
      ? { ...detail, data: { ...detail.data, tag_overlap: [] } }
      : list;
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });
  render(wrap(<ProfileView user="u1" range="30d" onUser={noop} onRange={noop} />));
  await waitFor(() => expect(screen.getByText(/Alpha/)).toBeInTheDocument());
  expect(screen.queryByText(/overlap with others/i)).not.toBeInTheDocument();
});

test("plugin-absent shows the enable-plugin notice", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const url = String(input);
    const body = url.includes("/api/profile/")
      ? detail
      : { ...list, data: { ...list.data, plugin_available: false } };
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });
  render(wrap(<ProfileView user="u1" range="30d" onUser={noop} onRange={noop} />));
  await waitFor(() =>
    expect(screen.getByText(/Playback Reporting not detected/i)).toBeInTheDocument(),
  );
});
