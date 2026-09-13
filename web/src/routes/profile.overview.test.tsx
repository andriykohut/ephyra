import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { ActivityDay, Envelope, PlaysPage, ProfileOverview } from "@/api/types";
import { ActivityStrip } from "@/components/ActivityStrip";
import { Art } from "@/components/Art";
import { ProfileOverviewView } from "./profile.overview";

function localISODate(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

afterEach(() => vi.restoreAllMocks());

test("shows a placeholder instead of a broken image when art 404s", async () => {
  render(<Art src="/api/art/person?name=Ghost" fallback="Ghost" />);
  fireEvent.error(screen.getByRole("img"));
  expect(await screen.findByText("G")).toBeInTheDocument();
});

const base: ProfileOverview = {
  user: { id: "u1", name: "alice" },
  since: "",
  plays: 0,
  watch_sec: 0,
  activity: [],
  people: [],
  items: [],
  genres: [],
  now_playing: null,
};

function renderOverview(overrides: Partial<ProfileOverview>) {
  const body: Envelope<ProfileOverview> = {
    data: { ...base, ...overrides },
    meta: { generated_at: new Date().toISOString(), stale: false },
  };
  const playsBody: Envelope<PlaysPage> = {
    data: { plays: [], next_cursor: null },
    meta: { generated_at: new Date().toISOString(), stale: false },
  };
  // ProfileOverviewView now also drives PlayList's own /plays request; route
  // by URL so that fetch doesn't get handed the overview envelope's shape.
  vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const resBody = String(input).includes("/plays") ? playsBody : body;
    return Promise.resolve(new Response(JSON.stringify(resBody), { status: 200 }));
  });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ProfileOverviewView userID="u1" range="30d" library="" />
    </QueryClientProvider>,
  );
}

test("renders the three headline numbers", async () => {
  renderOverview({ plays: 412, watch_sec: 360000, since: "2024-02-11 20:14:00" });
  expect(await screen.findByText("412")).toBeInTheDocument();
  expect(screen.getByText(/100h/)).toBeInTheDocument();
  expect(screen.getByText(/2024/)).toBeInTheDocument();
});

test("renders a clean header for a user with no plays, no since and no activity", async () => {
  renderOverview({});
  expect(await screen.findByText("0")).toBeInTheDocument();
  expect(screen.getByText("0h")).toBeInTheDocument();
  expect(screen.getByText("—")).toBeInTheDocument();
  expect(screen.queryByText(/NaN/)).not.toBeInTheDocument();
});

test("activity strip renders a full 53-week grid even with no history at all", () => {
  const { container } = render(<ActivityStrip days={[]} />);
  expect(container.querySelectorAll('[role="img"] > span').length).toBe(53 * 7);
});

test("activity strip colors today's cell instead of leaving it at the base level", () => {
  const today = localISODate(new Date());
  const days: ActivityDay[] = [{ day: today, watch_sec: 500 }];
  const { container } = render(<ActivityStrip days={days} />);
  const cell = container.querySelector(`span[title="${today}"]`);
  expect(cell).not.toBeNull();
  expect(cell?.getAttribute("style")).toContain("color-mix");
});
