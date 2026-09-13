import { Art } from "./Art";

export function Avatar({ userId, name }: { userId: string; name: string }) {
  return (
    <Art
      src={`/api/art/user/${userId}`}
      kind="avatar"
      fallback={name || "?"}
      className="h-[60px] w-[60px] text-[22px]"
    />
  );
}
