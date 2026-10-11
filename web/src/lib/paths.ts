// Addresses of Control Center's pages. Those still server-rendered (step
// 20R) are plain links: a full page load, not the UI's router.

const seg = encodeURIComponent;

const runBase = (c: string, ns: string, id: string) => `/clusters/${seg(c)}/runs/${seg(ns)}/${seg(id)}`;

/** artifactUrl is a run's stored file, served sandboxed by Control Center. */
export function artifactUrl(c: string, ns: string, id: string, path: string, download = false): string {
  const p = path.split("/").map(seg).join("/");
  return `${runBase(c, ns, id)}/artifacts/${p}${download ? "?download=1" : ""}`;
}

export const goPage = {
  logs: (c: string, ns: string, id: string) => `${runBase(c, ns, id)}/logs.txt`,
  newTest: (c: string, ns?: string) => `/clusters/${seg(c)}/tests/new` + (ns ? `?namespace=${seg(ns)}` : ""),
  duplicate: (c: string, ns: string, name: string) =>
    `/clusters/${seg(c)}/tests/new?namespace=${seg(ns)}&from=${seg(name)}`,
  test: (c: string, ns: string, name: string) => `/clusters/${seg(c)}/tests/${seg(ns)}/${seg(name)}`,
  edit: (c: string, ns: string, name: string) => `${goPage.test(c, ns, name)}/edit`,
};
