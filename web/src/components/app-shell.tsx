import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { meQuery } from "@/api/queries";
import { Notice } from "@/components/notice";
import { ThemeToggle } from "@/components/theme-toggle";
import { Badge } from "@/components/ui/badge";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";

export function AppShell({ children }: { children: ReactNode }) {
  const me = useQuery(meQuery);
  return (
    <div className="min-h-screen">
      <header className="border-b bg-card">
        <div className="mx-auto flex h-14 max-w-6xl items-center gap-4 px-4">
          <Link to="/" className="font-semibold tracking-tight">
            kubetest <span className="text-muted-foreground font-normal">Control Center</span>
          </Link>
          <div className="ml-auto flex items-center gap-2 text-sm">
            {me.data && (
              <span className="flex items-center gap-2" data-testid="me">
                <span className="text-muted-foreground">{me.data.email || "anonymous"}</span>
                <Badge variant="secondary">{me.data.role}</Badge>
              </span>
            )}
            <ThemeToggle />
          </div>
        </div>
      </header>
      <main className="mx-auto max-w-6xl px-4 py-6">
        <TooltipProvider>{children}</TooltipProvider>
      </main>
      <Toaster />
      <Notice />
    </div>
  );
}
