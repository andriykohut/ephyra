import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { Envelope, Profile, ProfileList, ProfileOverview } from "@/api/types";
import { Route as rootRoute } from "./__root";
import { LegacyRoute, Route as profileRoute } from "./profile";

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

const overview: Envelope<ProfileOverview> = {
  data: {
    user: { id: "u1", name: "alice" },
    since: "2025-01-01 00:00:00",
    plays: 12,
    watch_sec: 9000,
    activity: [],
    people: [],
    items: [],
    genres: [],
    now_playing: null,
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
    completion: [{ scope: "movie", bucket: "finished", count: 3 }],
    abandoned: [],
    rewatch: [],
    binge: [],
    taste: {
      genre: [],
      decade: [],
      length: [],
      tag: [],
      signature_genres: [],
      signature_tags: [],
    },
    baseline: { genre: [], decade: [], length: [], tag: [] },
    tag_overlap: [],
  },
  meta: { generated_at: new Date().toISOString(), stale: false },
};

const emptyPlays = {
  data: { plays: [], next_cursor: null },
  meta: { generated_at: new Date().toISOString(), stale: false },
};
const emptyLibraries = {
  data: [],
  meta: { generated_at: new Date().toISOString(), stale: false },
};

function mockApi() {
  vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const url = String(input);
    // Order matters: "/plays" and "/overview" are both substrings that would
    // otherwise also match the plainer "/api/profile/" check below.
    const body = url.includes("/api/libraries")
      ? emptyLibraries
      : url.includes("/plays")
        ? emptyPlays
        : url.includes("/overview")
          ? overview
          : url.includes("/api/profile/")
            ? detail
            : list;
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });
}

let router: ReturnType<typeof createRouter>;

function renderProfileAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const routeTree = rootRoute.addChildren([profileRoute, LegacyRoute]);
  router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}

function currentPath() {
  return router.state.location.pathname;
}
function currentSearch() {
  return router.state.location.search as Record<string, unknown>;
}

afterEach(() => vi.restoreAllMocks());

test("defaults to the overview tab", async () => {
  mockApi();
  renderProfileAt("/profile/u1");
  expect(await screen.findByRole("tab", { name: /overview/i })).toHaveAttribute(
    "aria-selected",
    "true",
  );
});

test("keeps the four analytical panels reachable under the stats tab", async () => {
  mockApi();
  renderProfileAt("/profile/u1");
  fireEvent.click(await screen.findByRole("tab", { name: /stats/i }));
  expect(await screen.findByText(/completion vs abandonment/i)).toBeInTheDocument();
  expect(screen.getByText(/rewatch vs first-watch/i)).toBeInTheDocument();
  expect(screen.getByText(/binge \/ marathon runs/i)).toBeInTheDocument();
  expect(screen.getByText(/taste fingerprint/i)).toBeInTheDocument();
});

test("redirects the old query-param URL to the path form", async () => {
  mockApi();
  renderProfileAt("/profile?user=u1&range=90d");
  await waitFor(() => expect(currentPath()).toBe("/profile/u1"));
  expect(currentSearch().range).toBe("90d");
});
