import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, Link, Outlet } from "@tanstack/react-router";

import { AppShell } from "@/components/app-shell";
import { ErrorView } from "@/components/error-view";

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: () => (
    <AppShell>
      <Outlet />
    </AppShell>
  ),
  errorComponent: ({ error }) => (
    <AppShell>
      <ErrorView error={error} />
    </AppShell>
  ),
  notFoundComponent: () => (
    <div className="space-y-2">
      <h1 className="text-xl font-semibold">Page not found</h1>
      <p className="text-muted-foreground">
        Nothing lives at this address.{" "}
        <Link to="/" className="text-primary underline-offset-4 hover:underline">
          Back to clusters
        </Link>
      </p>
    </div>
  ),
});
