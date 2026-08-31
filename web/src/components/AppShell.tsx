import { Link, useRouterState } from "@tanstack/react-router";
import { BarChart3, Layers, PlayCircle, Trash2, UserRound } from "lucide-react";
import type { ReactNode } from "react";
import { Mark } from "@/components/Mark";
import { cn } from "@/lib/utils";

function Wordmark({ markSize = 18, text = "text-[19px]" }: { markSize?: number; text?: string }) {
  return (
    <span className="flex items-center gap-2">
      <Mark size={markSize} className="drop-shadow-[0_0_8px_rgba(79,224,216,0.4)]" />
      <span className={cn("font-display font-bold lowercase tracking-[-0.03em] text-ink", text)}>
        ephyra
      </span>
    </span>
  );
}

const NAV = [
  { to: "/library", label: "Library", icon: Layers },
  { to: "/watch", label: "Watch Stats", icon: BarChart3 },
  { to: "/profile", label: "Profiles", icon: UserRound },
  { to: "/now", label: "Now Playing", icon: PlayCircle },
  { to: "/cleanup", label: "Cleanup", icon: Trash2 },
] as const;

export function AppShell({ children }: { children: ReactNode }) {
  const path = useRouterState({ select: (s) => s.location.pathname });

  return (
    <div className="ambient min-h-dvh">
      <div className="mx-auto min-h-dvh max-w-[1180px] md:grid md:grid-cols-[212px_1fr]">
        <aside className="hidden border-r border-line/70 px-4 py-6 md:block">
          <div className="mb-8 px-2">
            <Wordmark />
          </div>
          <nav className="flex flex-col gap-1">
            {NAV.map(({ to, label, icon: Icon }) => {
              const active = path.startsWith(to);
              return (
                <Link
                  key={to}
                  to={to}
                  className={cn(
                    "flex items-center gap-2.5 rounded-[10px] px-3 py-2 text-[13.5px] transition-colors",
                    active ? "bg-panel/70 font-medium text-ink" : "text-muted hover:text-ink",
                  )}
                >
                  <Icon size={15} strokeWidth={1.75} />
                  {label}
                </Link>
              );
            })}
          </nav>
        </aside>

        <main className="min-w-0 px-5 py-6 md:px-9 md:py-8">
          <div className="mb-5 flex items-center gap-4 md:hidden">
            <Wordmark markSize={16} text="text-lg" />
            <nav className="flex gap-1 overflow-x-auto">
              {NAV.map(({ to, label }) => {
                const active = path.startsWith(to);
                return (
                  <Link
                    key={to}
                    to={to}
                    className={cn(
                      "whitespace-nowrap rounded-full px-3 py-1 text-[12px]",
                      active ? "bg-panel/70 text-ink" : "text-muted",
                    )}
                  >
                    {label}
                  </Link>
                );
              })}
            </nav>
          </div>
          {children}
        </main>
      </div>
    </div>
  );
}
