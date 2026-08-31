import { createRootRoute, Outlet } from "@tanstack/react-router";
import { AppShell } from "@/components/AppShell";
import { NotFound } from "@/components/NotFound";

export const Route = createRootRoute({
  component: () => (
    <AppShell>
      <Outlet />
    </AppShell>
  ),
  // Renders inside the root <Outlet/>, so AppShell is already around it.
  notFoundComponent: NotFound,
});
