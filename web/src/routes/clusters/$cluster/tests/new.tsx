import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { z } from "zod";

import type { TestForm } from "@/api/client";
import { clustersQuery, meQuery, testFormQuery } from "@/api/queries";
import { renamed, TestWizard } from "@/components/editor/wizard";
import { ErrorView } from "@/components/error-view";
import { PageHeader } from "@/components/page-header";
import { Skeleton } from "@/components/ui/skeleton";

const search = z.object({
  namespace: z.string().optional().catch(undefined),
  from: z.string().optional().catch(undefined),
});

export const Route = createFileRoute("/clusters/$cluster/tests/new")({
  validateSearch: search,
  component: NewTestPage,
});

function NewTestPage() {
  const { cluster } = Route.useParams();
  const { namespace, from } = Route.useSearch();
  const clusters = useQuery(clustersQuery);
  const me = useQuery(meQuery);
  const clusterTitle = clusters.data?.find((c) => c.name === cluster)?.displayName ?? cluster;
  // Duplicate: the form of the Test copied, under a new name.
  const source = useQuery({
    ...testFormQuery({ cluster, ns: namespace ?? "", name: from ?? "" }),
    enabled: !!from && !!namespace,
  });

  let body;
  if (me.data && !me.data.can.run) {
    body = (
      <p role="status" className="text-muted-foreground">
        Creating a Test needs the developer role; you are signed in as {me.data.role}.
      </p>
    );
  } else if (from && namespace) {
    if (source.isPending) body = <Skeleton className="h-96" />;
    else if (source.isError) body = <ErrorView error={source.error} />;
    else {
      const name = `${from}-copy`;
      const f = source.data.form;
      const initial: TestForm = {
        ...f,
        name,
        resourceVersion: "",
        generation: undefined,
        yaml: f.yaml && renamed(f.yaml, from, name),
      };
      body = <TestWizard key={from} cluster={cluster} initial={initial} yamlOnly={source.data.yamlOnly} />;
    }
  } else {
    body = (
      <TestWizard
        key="new"
        cluster={cluster}
        initial={{ name: "", namespace: namespace ?? "", mode: "form" }}
      />
    );
  }

  return (
    <div>
      <PageHeader
        crumbs={[
          { label: "Clusters", to: "/" },
          { label: clusterTitle, to: "/clusters/$cluster", params: { cluster } },
          { label: from ? `Duplicate ${from}` : "New test" },
        ]}
        title={from ? `Duplicate ${from}` : "New test"}
      />
      <p className="text-muted-foreground -mt-4 mb-6 text-sm">
        The Test is created in the cluster, managed in the GUI — or download its YAML for Git (ArgoCD).
      </p>
      {body}
    </div>
  );
}
