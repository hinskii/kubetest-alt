import { ApiError } from "@/api/client";

export function ErrorView({ error }: { error: unknown }) {
  const message = error instanceof Error ? error.message : String(error);
  const status = error instanceof ApiError ? ` (${error.status})` : "";
  return (
    <div role="alert" className="rounded-xl border bg-danger-soft p-4 text-danger">
      <p className="font-semibold">Something went wrong{status}</p>
      <p className="mt-1 text-sm">{message}</p>
    </div>
  );
}
