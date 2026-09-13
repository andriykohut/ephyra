import { render, screen, within } from "@testing-library/react";
import { expect, test } from "vitest";
import { RankGrid, type RankRow } from "./RankGrid";

test("shows the played-only total when the finished-only toggle flips", async () => {
  const rows: RankRow[] = [
    {
      person: "Ada Vex",
      kind: "actor",
      watch_sec: 9000,
      watch_sec_played: 100,
      distinct_titles: 3,
      distinct_titles_played: 1,
      plays: 5,
      plays_played: 1,
      jf_url: "",
    },
  ];
  render(<RankGrid rows={rows} metric="watch_sec" playedOnly={false} />);
  expect(await screen.findByText(/2h 30m/)).toBeInTheDocument();

  render(<RankGrid rows={rows} metric="watch_sec" playedOnly={true} />);
  expect(await screen.findByText(/1m 40s/)).toBeInTheDocument();
});

test("renders an unresolved name as plain text, not a link", async () => {
  render(
    <RankGrid
      rows={[{ person: "Ghost", kind: "actor", watch_sec: 900, jf_url: "" } as never]}
      metric="watch_sec"
      playedOnly={false}
    />,
  );
  expect(screen.queryByRole("link", { name: "Ghost" })).toBeNull();
  expect(await screen.findByText("Ghost")).toBeInTheDocument();
});

test("ranks by breadth instead of hours when the metric switches", () => {
  const rows: RankRow[] = [
    {
      person: "Binged Regular",
      kind: "actor",
      watch_sec: 50000,
      watch_sec_played: 50000,
      distinct_titles: 1,
      distinct_titles_played: 1,
      plays: 40,
      plays_played: 40,
      jf_url: "",
    },
    {
      person: "Widely Seen",
      kind: "actor",
      watch_sec: 3000,
      watch_sec_played: 3000,
      distinct_titles: 8,
      distinct_titles_played: 8,
      plays: 8,
      plays_played: 8,
      jf_url: "",
    },
  ];

  const byHours = render(<RankGrid rows={rows} metric="watch_sec" playedOnly={false} />);
  const hoursOrder = within(byHours.container)
    .getAllByText(/Binged Regular|Widely Seen/)
    .map((el) => el.textContent);
  expect(hoursOrder[0]).toBe("Binged Regular");
  byHours.unmount();

  const byBreadth = render(<RankGrid rows={rows} metric="distinct_titles" playedOnly={false} />);
  const breadthOrder = within(byBreadth.container)
    .getAllByText(/Binged Regular|Widely Seen/)
    .map((el) => el.textContent);
  expect(breadthOrder[0]).toBe("Widely Seen");
});

test("gives a genre row no face chip, unlike a person row", () => {
  const rows: RankRow[] = [
    { person: "Ada Vex", kind: "actor", watch_sec: 900, jf_url: "" },
    { person: "Noir", kind: "genre", watch_sec: 500, jf_url: "" },
  ];
  render(<RankGrid rows={rows} metric="watch_sec" playedOnly={false} />);
  expect(screen.getByRole("img", { name: "Ada Vex" })).toBeInTheDocument();
  expect(screen.queryByRole("img", { name: "Noir" })).toBeNull();
});

test("shows a message instead of an empty panel when there are no rows", () => {
  render(<RankGrid rows={[]} metric="watch_sec" playedOnly={false} empty="No actors yet." />);
  expect(screen.getByText("No actors yet.")).toBeInTheDocument();
});

test("shows the empty state, not zero bars, when playedOnly leaves every row at 0", () => {
  const rows: RankRow[] = [
    { person: "Ada Vex", kind: "actor", watch_sec: 9000, watch_sec_played: 0, jf_url: "" },
    { person: "Bo Reed", kind: "actor", watch_sec: 5000, watch_sec_played: 0, jf_url: "" },
  ];
  render(<RankGrid rows={rows} metric="watch_sec" playedOnly={true} empty="No actors yet." />);
  expect(screen.getByText("No actors yet.")).toBeInTheDocument();
  expect(screen.queryByText("Ada Vex")).toBeNull();
});
