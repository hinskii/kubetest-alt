import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = { vus: 2, iterations: 20 };

// The catalog e2e mounts the paths to request as test data (a ConfigMap
// in content.testData) and names it in CATALOG_TESTDATA: then they MUST
// be there — a missing mount fails the run. The git sample runs without.
const data = __ENV.CATALOG_TESTDATA;
const [root, missing] = data
  ? open(`${__ENV.KUBETEST_TESTDATA_DIR}/${data}/paths.txt`).split('\n').filter(Boolean)
  : ['/', '/missing'];

export default function () {
  check(http.get(`http://target:8000${root}`), { 'root is 200': (r) => r.status === 200 });
  check(http.get(`http://target:8000${missing}`), { 'missing is 200': (r) => r.status === 200 });
  // ~3s in total: k6 writes the web-dashboard report only after two
  // aggregation periods (case.yaml sets dashboardPeriod: 1s).
  sleep(0.3);
}
