import { useState } from "react";
import { cn } from "@/lib/utils";

type ArtKind = "avatar" | "chip" | "poster";

const SHAPE: Record<ArtKind, string> = {
  avatar: "rounded-full",
  chip: "rounded-full",
  poster: "rounded-[4px]",
};

// Roughly half the people in a real library have no portrait -- Jellyfin
// 404s the request -- so the initials fallback is a normal render path, not
// an error state, and must never flash a broken-image icon.
export function Art({
  src,
  kind = "chip",
  fallback,
  className,
}: {
  src?: string;
  kind?: ArtKind;
  fallback: string;
  className?: string;
}) {
  const [broken, setBroken] = useState(false);
  const initial = fallback.trim().charAt(0).toUpperCase() || "?";

  if (!src || broken) {
    return (
      <div
        role="img"
        aria-label={fallback}
        className={cn(
          "flex shrink-0 items-center justify-center overflow-hidden font-display font-semibold",
          SHAPE[kind],
          kind === "avatar"
            ? "bg-gradient-to-br from-cyan to-violet text-abyss"
            : "bg-line text-muted",
          className,
        )}
      >
        {initial}
      </div>
    );
  }

  return (
    <img
      src={src}
      alt={fallback}
      onError={() => setBroken(true)}
      className={cn("object-cover", SHAPE[kind], className)}
    />
  );
}
