import http from 'k6/http';
import { check } from 'k6';

export const options = { vus: 2, iterations: 20 };

export default function () {
  check(http.get('http://target.kubetest-catalog.svc:8000/'), { 'root is 200': (r) => r.status === 200 });
  check(http.get('http://target.kubetest-catalog.svc:8000/missing'), { 'missing is 200': (r) => r.status === 200 });
}
