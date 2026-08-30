import { queryOptions } from "@tanstack/react-query";
import { fetchEnvelope } from "./client";
import type { LibraryOverview } from "./types";

export const libraryOverviewQuery = () =>
  queryOptions({
    queryKey: ["library-overview"],
    queryFn: () => fetchEnvelope<LibraryOverview>("/api/library/overview"),
    staleTime: 30 * 60 * 1000,
  });
