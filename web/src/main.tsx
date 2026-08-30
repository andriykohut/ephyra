import "@fontsource-variable/bricolage-grotesque";
import "@fontsource/ibm-plex-sans/400.css";
import "@fontsource/ibm-plex-sans/500.css";
import "@fontsource/ibm-plex-sans/600.css";
import "@fontsource/ibm-plex-mono/400.css";
import "@fontsource/ibm-plex-mono/500.css";
import "@fontsource/ibm-plex-mono/600.css";
import "./index.css";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { router } from "./router";

const qc = new QueryClient({
  defaultOptions: {
    queries: { retry: 1, refetchOnWindowFocus: true, staleTime: 60_000 },
  },
});

const el = document.getElementById("root");
if (!el) throw new Error("#root not found");

createRoot(el).render(
  <StrictMode>
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
