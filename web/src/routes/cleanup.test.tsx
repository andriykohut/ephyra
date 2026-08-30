import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { Cleanup, Envelope } from "@/api/types";
import { CleanupView } from "./cleanup";

const sample: Envelope<Cleanup> = {
  data: {
    mode: "never",
    reclaimable_bytes: 3_221_225_472,
    match_count: 2,
    truncated: false,
    items: [
      {
        item_id: "s1",
        scope: "series",
        name: "Old Show",
        library: "Shows",
        bytes: 2_147_483_648,
        episodes: 24,
        added_at: "2020-01-01T00:00:00Z",
        last_played_at: null,
      },
      {
        item_id: "m1",
        scope: "movie",
        name: "Unseen Film",
        library: "Movies",
        bytes: 1_073_741_824,
        episodes: 0,
        added_at: "2021-01-01T00:00:00Z",
        last_played_at: null,
      },
    ],
  },
  meta: { generated_at: new Date().toISOString(), stale: false },
};

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

const noop = () => {};

afterEach(() => vi.restoreAllMocks());

test("renders the reclaimable header and the candidate rows", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify(sample), { status: 200 }),
  );
  render(wrap(<CleanupView mode="never" sort="size" onMode={noop} onSort={noop} />));
  await waitFor(() => expect(screen.getByText(/Old Show/)).toBeInTheDocument());
  expect(screen.getByText(/24 eps/)).toBeInTheDocument();
  expect(screen.getByText(/never deletes/i)).toBeInTheDocument();
  expect(screen.getByText("3.0 GB")).toBeInTheDocument();
  const csv = screen.getByRole("link", { name: /export csv/i });
  expect(csv).toHaveAttribute("href", expect.stringContaining("format=csv"));
  expect(csv).toHaveAttribute("href", expect.stringContaining("mode=never"));
});

test("empty state", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(
      JSON.stringify({
        ...sample,
        data: { ...sample.data, items: [], match_count: 0, reclaimable_bytes: 0 },
      }),
      { status: 200 },
    ),
  );
  render(wrap(<CleanupView mode="never" sort="size" onMode={noop} onSort={noop} />));
  await waitFor(() => expect(screen.getByText(/it's all been watched/i)).toBeInTheDocument());
});
