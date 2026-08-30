import type { Envelope } from "./types";

export class ApiError extends Error {
  status: number;
  code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export async function fetchEnvelope<T>(path: string): Promise<Envelope<T>> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    throw new ApiError(
      res.status,
      body?.error?.code ?? "http_error",
      body?.error?.message ?? `request failed (${res.status})`,
    );
  }
  return body as Envelope<T>;
}
