import { Link } from "@tanstack/react-router";
import { Fragment, type ReactNode } from "react";

import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";

export type Crumb = { label: string; to?: string; params?: Record<string, string> };

/** PageHeader: where we are (breadcrumbs), the title and its actions. */
export function PageHeader({
  crumbs,
  title,
  children,
}: {
  crumbs: Crumb[];
  title: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="mb-6 space-y-2">
      <Breadcrumb>
        <BreadcrumbList>
          {crumbs.map((c, i) => (
            <Fragment key={i}>
              {i > 0 && <BreadcrumbSeparator />}
              <BreadcrumbItem>
                {c.to ? (
                  <BreadcrumbLink asChild>
                    <Link to={c.to} params={c.params}>
                      {c.label}
                    </Link>
                  </BreadcrumbLink>
                ) : (
                  <BreadcrumbPage>{c.label}</BreadcrumbPage>
                )}
              </BreadcrumbItem>
            </Fragment>
          ))}
        </BreadcrumbList>
      </Breadcrumb>
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="mr-auto text-2xl font-semibold tracking-tight">{title}</h1>
        {children}
      </div>
    </div>
  );
}
