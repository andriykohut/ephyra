import { QueryClient } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import { profileOverviewQuery, profileQuery } from "./queries";

afterEach(() => vi.restoreAllMocks());

// An unencoded interpolation would send "library=Movies " plus a stray
// "Kids" param, and the handler would match nothing.
const trickyLibrary = "Movies & Kids +Sci+Fi";

function fetchedURL(run: () => Promise<unknown>) {
  let seen = "";
  vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    seen = String(input);
    return Promise.resolve(new Response(JSON.stringify({ data: null, meta: {} }), { status: 200 }));
  });
  return run().then(() => seen);
}

test("profileOverviewQuery URL-encodes the library param", async () => {
  const qc = new QueryClient();
  const url = await fetchedURL(() =>
    qc.fetchQuery(profileOverviewQuery("u1", "30d", trickyLibrary)),
  );
  const params = new URL(url, "http://x").searchParams;
  expect(params.get("library")).toBe(trickyLibrary);
});

test("profileQuery URL-encodes the library param like its profilePlaysQuery sibling", async () => {
  const qc = new QueryClient();
  const url = await fetchedURL(() => qc.fetchQuery(profileQuery("u1", "30d", trickyLibrary)));
  const params = new URL(url, "http://x").searchParams;
  expect(params.get("library")).toBe(trickyLibrary);
});
