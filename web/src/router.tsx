import { createRoute, createRouter, redirect } from "@tanstack/react-router";
import { Route as rootRoute } from "./routes/__root";
import { Route as cleanupRoute } from "./routes/cleanup";
import { Route as libraryRoute } from "./routes/library";
import { Route as nowRoute } from "./routes/now";
import { Route as profileRoute } from "./routes/profile";
import { Route as watchRoute } from "./routes/watch";

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/library" });
  },
});

const routeTree = rootRoute.addChildren([
  indexRoute,
  libraryRoute,
  watchRoute,
  profileRoute,
  nowRoute,
  cleanupRoute,
]);

export const router = createRouter({ routeTree, defaultPreload: "intent" });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
