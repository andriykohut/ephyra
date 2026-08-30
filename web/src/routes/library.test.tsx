import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { Envelope, LibraryOverview as LO } from "@/api/types";

vi.mock("@/charts/EChart", () => ({
  EChart: () => <div data-testid="echart" />,
}));

const { LibraryOverview } = await import("./library");

const sample: Envelope<LO> = {
  data: {
    totals: {
      items_by_library: [{ label: "Movies", count: 1240 }],
      runtime_seconds: 5_270_400,
      bytes: 30_100_000_000_000,
      count_uhd: 312,
      count_hdr: 540,
      count_dv: 47,
      series: 214,
      items: 11_120,
    },
    disk_by_resolution: [{ bucket: "4K", bytes: 12e12, items: 300 }],
    disk_by_codec: [{ bucket: "HEVC", bytes: 18e12, items: 400 }],
    disk_by_container: [{ bucket: "mkv", bytes: 25e12, items: 900 }],
    disk_by_library: [{ bucket: "Movies", bytes: 21e12, items: 1240 }],
    genres_top: [
      { label: "Drama", count: 3100 },
      { label: "Comedy", count: 2400 },
    ],
    by_decade: [{ label: "2010s", count: 2900 }],
    growth: [{ month: "2024-01", added_items: 40, added_bytes: 1e9, cum_items: 8000 }],
  },
  meta: { generated_at: new Date().toISOString(), stale: false },
};

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

afterEach(() => vi.restoreAllMocks());

test("renders heading, stats, genre band and charts", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify(sample), { status: 200 }),
  );
  render(wrap(<LibraryOverview />));

  await waitFor(() => expect(screen.getByRole("heading", { name: "Library" })).toBeInTheDocument());
  expect(screen.getAllByText("11,120").length).toBeGreaterThan(0);
  expect(screen.getByText(/drama/i)).toBeInTheDocument();
  expect(screen.queryByRole("status")).not.toBeInTheDocument();
  // charts are lazy-loaded, so wait for them
  expect((await screen.findAllByTestId("echart")).length).toBeGreaterThanOrEqual(5);
});

test("shows the stale banner when meta.stale", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify({ ...sample, meta: { ...sample.meta, stale: true } }), {
      status: 200,
    }),
  );
  render(wrap(<LibraryOverview />));
  await waitFor(() => expect(screen.getByRole("status")).toBeInTheDocument());
});

test("shows an error state on 503", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(
      JSON.stringify({ error: { code: "not_ready", message: "first refresh has not completed" } }),
      { status: 503 },
    ),
  );
  render(wrap(<LibraryOverview />));
  await waitFor(() => expect(screen.getByText(/first refresh/i)).toBeInTheDocument());
});
