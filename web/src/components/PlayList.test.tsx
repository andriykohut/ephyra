import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import type { Envelope, NowSession, PlayRow, PlaysPage } from "@/api/types";
import { PlayList } from "./PlayList";

let ioCallback: IntersectionObserverCallback | null = null;
class FakeIntersectionObserver {
  constructor(cb: IntersectionObserverCallback) {
    ioCallback = cb;
  }
  observe() {}
  unobserve() {}
  disconnect() {}
}
vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);

function triggerIntersection() {
  ioCallback?.([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver);
}

function play(id: string, over: Partial<PlayRow> = {}): PlayRow {
  return {
    at: "2026-09-01 10:00:00",
    item_id: id,
    item_type: "movie",
    name: id,
    series_name: "",
    play_duration_sec: 100,
    played: false,
    jf_url: "",
    ...over,
  };
}

function stubPlays(pages: PlaysPage[]) {
  let call = 0;
  return vi.spyOn(globalThis, "fetch").mockImplementation(() => {
    const page = pages[Math.min(call, pages.length - 1)];
    call++;
    const body: Envelope<PlaysPage> = {
      data: page,
      meta: { generated_at: new Date().toISOString(), stale: false },
    };
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });
}

function renderList(props: Partial<Parameters<typeof PlayList>[0]> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PlayList userId="u1" library="" {...props} />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  ioCallback = null;
  vi.restoreAllMocks();
});

test("loads the next page when the sentinel scrolls into view", async () => {
  const fetchSpy = stubPlays([
    { plays: [play("a"), play("b")], next_cursor: { at: "2026-09-01 10:00:00", row_id: 7 } },
    { plays: [play("c")], next_cursor: null },
  ]);
  renderList();

  expect(await screen.findByText("a")).toBeInTheDocument();
  triggerIntersection();
  expect(await screen.findByText("c")).toBeInTheDocument();
  expect(fetchSpy).toHaveBeenCalledTimes(2);
});

test("stops asking once the cursor comes back null", async () => {
  const fetchSpy = stubPlays([{ plays: [play("a")], next_cursor: null }]);
  renderList();
  await screen.findByText("a");
  triggerIntersection();
  triggerIntersection();
  expect(fetchSpy).toHaveBeenCalledTimes(1);
});

test("pins a live session to the top with a live pill, ahead of history", async () => {
  stubPlays([{ plays: [play("a")], next_cursor: null }]);
  const nowPlaying: NowSession = {
    session_id: "s1",
    user: "alice",
    type: "Movie",
    title: "Live Movie",
    series: "",
    season_episode: "",
    item_id: "live1",
    art: { primary_tag: "", backdrop: null },
    play_method: "DirectPlay",
    paused: false,
    position_sec: 10,
    runtime_sec: 100,
    progress_pct: 10,
    is_remote: false,
    client: "",
    device: "",
    source: {
      video: { codec: "", width: 0, height: 0, range: "", bitrate: 0 },
      audio: { codec: "", channels: 0, layout: "", bitrate: 0 },
    },
    transcode: null,
  };
  renderList({ nowPlaying });

  const live = await screen.findByText("live");
  const historyRow = await screen.findByText("a");
  expect(live.compareDocumentPosition(historyRow) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(screen.getByText("Live Movie")).toBeInTheDocument();
});

test("links the title to Jellyfin when jf_url is set, plain text otherwise", async () => {
  stubPlays([
    {
      plays: [
        play("linked", { jf_url: "http://jf.example/web/#/details?id=linked" }),
        play("bare"),
      ],
      next_cursor: null,
    },
  ]);
  renderList();

  const linked = await screen.findByRole("link", { name: "linked" });
  expect(linked).toHaveAttribute("href", "http://jf.example/web/#/details?id=linked");
  expect(screen.queryByRole("link", { name: "bare" })).toBeNull();
  expect(screen.getByText("bare")).toBeInTheDocument();
});

test("falls back to the placeholder on a poster 404 without guessing why it's missing", async () => {
  stubPlays([{ plays: [play("no-art-item")], next_cursor: null }]);
  renderList();

  const poster = await screen.findByRole("img", { name: "no-art-item" });
  fireEvent.error(poster);

  // Falls back to Art's initials placeholder (still role="img", same name)
  // rather than announcing a cause a poster 404 can't actually confirm.
  expect(await screen.findByRole("img", { name: "no-art-item" })).toBeInTheDocument();
  expect(screen.getByText("movie")).toBeInTheDocument();
  expect(screen.queryByText(/no longer/)).toBeNull();
});
