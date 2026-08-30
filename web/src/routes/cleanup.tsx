import { createRoute } from "@tanstack/react-router";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/cleanup",
  component: () => (
    <div>
      <h1 className="font-display text-2xl font-bold tracking-[-0.03em] text-ink">Cleanup</h1>
      <p className="mt-2 text-[14px] text-muted">Not in this build yet.</p>
    </div>
  ),
});
