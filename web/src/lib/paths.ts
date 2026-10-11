// Addresses of Control Center's pages. Those still server-rendered (step
// 20R) are plain links: a full page load, not the UI's router.

const seg = encodeURIComponent;

export const goPage = {
  run: (c: string, ns: string, id: string) => `/clusters/${seg(c)}/runs/${seg(ns)}/${seg(id)}`,
  schedules: (c: string) => `/clusters/${seg(c)}/schedules`,
  newTest: (c: string, ns?: string) => `/clusters/${seg(c)}/tests/new` + (ns ? `?namespace=${seg(ns)}` : ""),
  duplicate: (c: string, ns: string, name: string) =>
    `/clusters/${seg(c)}/tests/new?namespace=${seg(ns)}&from=${seg(name)}`,
  test: (c: string, ns: string, name: string) => `/clusters/${seg(c)}/tests/${seg(ns)}/${seg(name)}`,
  edit: (c: string, ns: string, name: string) => `${goPage.test(c, ns, name)}/edit`,
  cases: (c: string, ns: string, name: string) => `${goPage.test(c, ns, name)}/cases`,
  analytics: (c: string, ns: string, name: string) => `${goPage.test(c, ns, name)}/analytics`,
  cleanup: (c: string, ns: string, name: string) => `${goPage.test(c, ns, name)}/cleanup`,
};

/** withNotice adds the notice a page shows on load (as the Go pages do). */
export function withNotice(path: string, notice: string): string {
  return `${path}${path.includes("?") ? "&" : "?"}notice=${encodeURIComponent(notice)}`;
}
