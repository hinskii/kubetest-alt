#!/usr/bin/env bash
# Regenerates the report fixtures under pkg/report/*/testdata by running
# each catalog tool for real (same images as config/templates) against a
# throwaway HTTP target that answers "/" with 200 and "/missing" with 404,
# so every report contains both successes and failures.
#
# Usage: hack/report-fixtures.sh            # all tools
#        hack/report-fixtures.sh k6 locust  # a subset
#
# Fixtures are committed; this script is the evidence of where they came
# from and how to refresh them after a tool/image bump. Requires Docker.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NET="kt-report-fixtures"
TARGET="kt-report-target"
WORK="$(mktemp -d)"
trap 'docker rm -f "$TARGET" >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT

# Images: keep in sync with config/templates/*.yaml.
K6_IMAGE="grafana/k6:1.4.0"
JMETER_IMAGE="alpine/jmeter:5.6.3"
LOCUST_IMAGE="locustio/locust:2.42.1"
ARTILLERY_IMAGE="artilleryio/artillery:2.0.34"
GATLING_IMAGE="${GATLING_IMAGE:-kubetest-alt-gatling:3.9.5-smoke}" # build: docker build -t "$GATLING_IMAGE" executors/gatling

TOOLS=("$@")
[ ${#TOOLS[@]} -eq 0 ] && TOOLS=(k6 jmeter locust artillery gatling)

docker network create "$NET" >/dev/null
mkdir -p "$WORK/www"
echo ok >"$WORK/www/index.html"
docker run -d --rm --name "$TARGET" --network "$NET" -v "$WORK/www:/www:ro" -w /www \
  python:3.13-alpine python -m http.server 8000 >/dev/null
sleep 2
URL="http://$TARGET:8000"

run() { # run <image> <workdir-mount> <args...>
  local image="$1" dir="$2" name="kt-report-$RANDOM"; shift 2
  chmod -R a+rwX "$dir"
  # Hard cap: a misconfigured tool must not run away (a JMeter plan once
  # looped forever). Every fixture run finishes in well under a minute.
  ( sleep 180; docker rm -f "$name" >/dev/null 2>&1 ) &
  local guard=$!
  docker run --rm --name "$name" --network "$NET" -v "$dir:/work" -w /work "$image" "$@" || true
  kill "$guard" 2>/dev/null || true
}

for tool in "${TOOLS[@]}"; do
  d="$WORK/$tool"; mkdir -p "$d/out"
  echo "== $tool"
  case "$tool" in
  k6)
    cat >"$d/script.js" <<EOF
import http from 'k6/http';
import { check } from 'k6';
export const options = { vus: 2, iterations: 20 };
export default function () {
  check(http.get('$URL/'), { 'root is 200': (r) => r.status === 200 });
  check(http.get('$URL/missing'), { 'missing is 200': (r) => r.status === 200 });
}
EOF
    run "$K6_IMAGE" "$d" run --summary-export /work/out/summary.json /work/script.js
    cp "$d/out/summary.json" "$ROOT/pkg/report/k6/testdata/summary.json"
    ;;
  jmeter)
    cat >"$d/plan.jmx" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<jmeterTestPlan version="1.2" properties="5.0" jmeter="5.6.3">
  <hashTree>
    <TestPlan guiclass="TestPlanGui" testclass="TestPlan" testname="fixture"/>
    <hashTree>
      <ThreadGroup guiclass="ThreadGroupGui" testclass="ThreadGroup" testname="users">
        <intProp name="ThreadGroup.num_threads">2</intProp>
        <intProp name="ThreadGroup.ramp_time">0</intProp>
        <stringProp name="ThreadGroup.on_sample_error">continue</stringProp>
        <elementProp name="ThreadGroup.main_controller" elementType="LoopController" guiclass="LoopControlPanel" testclass="LoopController">
          <boolProp name="LoopController.continue_forever">false</boolProp>
          <stringProp name="LoopController.loops">5</stringProp>
        </elementProp>
      </ThreadGroup>
      <hashTree>
        <HTTPSamplerProxy guiclass="HttpTestSampleGui" testclass="HTTPSamplerProxy" testname="root">
          <stringProp name="HTTPSampler.domain">$TARGET</stringProp>
          <stringProp name="HTTPSampler.port">8000</stringProp>
          <stringProp name="HTTPSampler.path">/</stringProp>
          <stringProp name="HTTPSampler.method">GET</stringProp>
        </HTTPSamplerProxy>
        <hashTree/>
        <HTTPSamplerProxy guiclass="HttpTestSampleGui" testclass="HTTPSamplerProxy" testname="missing">
          <stringProp name="HTTPSampler.domain">$TARGET</stringProp>
          <stringProp name="HTTPSampler.port">8000</stringProp>
          <stringProp name="HTTPSampler.path">/missing</stringProp>
          <stringProp name="HTTPSampler.method">GET</stringProp>
        </HTTPSamplerProxy>
        <hashTree/>
      </hashTree>
    </hashTree>
  </hashTree>
</jmeterTestPlan>
EOF
    run "$JMETER_IMAGE" "$d" -n -t /work/plan.jmx -l /work/out/jmeter.jtl -j /work/out/jmeter.log
    cp "$d/out/jmeter.jtl" "$ROOT/pkg/report/jtl/testdata/jmeter.jtl"
    ;;
  locust)
    cat >"$d/locustfile.py" <<'EOF'
from locust import HttpUser, task, constant


class FixtureUser(HttpUser):
    wait_time = constant(0.1)

    @task
    def root(self):
        self.client.get("/")

    @task
    def missing(self):
        self.client.get("/missing")
EOF
    run "$LOCUST_IMAGE" "$d" --headless -u 2 -r 2 -t 5s --host "$URL" -f /work/locustfile.py --csv /work/out/locust
    cp "$d/out/locust_stats.csv" "$ROOT/pkg/report/locust/testdata/locust_stats.csv"
    ;;
  artillery)
    cat >"$d/scenario.yml" <<EOF
config:
  target: "$URL"
  phases:
    - duration: 3
      arrivalRate: 3
scenarios:
  - flow:
      - get: { url: "/" }
      - get: { url: "/missing" }
EOF
    run "$ARTILLERY_IMAGE" "$d" run /work/scenario.yml --output /work/out/report.json
    cp "$d/out/report.json" "$ROOT/pkg/report/artillery/testdata/report.json"
    ;;
  gatling)
    mkdir -p "$d/simulations"
    cat >"$d/simulations/FixtureSimulation.scala" <<EOF
import io.gatling.core.Predef._
import io.gatling.http.Predef._

class FixtureSimulation extends Simulation {
  val httpProtocol = http.baseUrl("$URL")
  val scn = scenario("fixture").repeat(5) {
    exec(http("root").get("/")).exec(http("missing").get("/missing"))
  }
  setUp(scn.inject(atOnceUsers(2))).protocols(httpProtocol)
}
EOF
    run "$GATLING_IMAGE" "$d" gatling-run -sf /work/simulations -rf /work/out -s FixtureSimulation -rm local
    stats="$(find "$d/out" -path '*/js/stats.json' | head -1)"
    cp "$stats" "$ROOT/pkg/report/gatling/testdata/stats.json"
    ;;
  *) echo "unknown tool: $tool" >&2; exit 2 ;;
  esac
done
echo "fixtures refreshed: ${TOOLS[*]}"
