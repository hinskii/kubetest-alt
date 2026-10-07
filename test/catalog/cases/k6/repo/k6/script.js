import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = { vus: 2, iterations: 20 };

export default function () {
  check(http.get('http://target:8000/'), { 'root is 200': (r) => r.status === 200 });
  check(http.get('http://target:8000/missing'), { 'missing is 200': (r) => r.status === 200 });
  // ~3s in total: k6 writes the web-dashboard report only after two
  // aggregation periods (case.yaml sets dashboardPeriod: 1s).
  sleep(0.3);
}
