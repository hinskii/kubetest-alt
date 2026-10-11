import { useRouter, useRouterState } from "@tanstack/react-router";
import { useEffect } from "react";
import { toast } from "sonner";

/**
 * Notice shows the ?notice= a server-rendered page's action redirected
 * with (e.g. "Test saved.") as a toast, then drops it from the address.
 */
export function Notice() {
  const router = useRouter();
  const href = useRouterState({ select: (s) => s.location.href });
  useEffect(() => {
    const url = new URL(href, window.location.origin);
    const notice = url.searchParams.get("notice");
    if (!notice) return;
    toast(notice.slice(0, 300));
    url.searchParams.delete("notice");
    router.history.replace(url.pathname + url.search + url.hash);
  }, [href, router]);
  return null;
}
