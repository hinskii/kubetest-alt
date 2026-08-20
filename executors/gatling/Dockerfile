# kubetest-alt platform image — Gatling load-test runner.
#
# EXCEPTIONAL: this is the SECOND platform-built image in the whole
# operator (after executors/content-fetcher/), and the ONLY tool image
# the platform bundles. Every other tool in config/templates/ points at
# a vendor-published image. Rationale (CLAUDE.md §3 update):
#
#   * No arm64-native community Gatling image existed at step 15A commit
#     time; `denvazh/gatling:latest` is Gatling 2.3.1 (linux/amd64,
#     emulated on arm64) and its assertion violations reported exit=0 —
#     failing the "verdict verified empirically" catalog rule.
#   * Gatling ≥3.x propagates assertion failures to exit code; the
#     bundled `gatling.sh` returns 2 on assertion failure.
#   * A platform image bundled from eclipse-temurin + the official
#     Maven Central bundle zip is the cleanest way to ship this today.
#
# Pinned to Gatling 3.9.5 — the last 3.9.x line, arm64+amd64 compatible
# via JVM. Bump requires: retest the failing-assertion smoke and update
# the tag in config/templates/gatling.yaml.

FROM eclipse-temurin:21-jre

ARG GATLING_VERSION=3.9.5
ARG GATLING_BUNDLE=gatling-charts-highcharts-bundle-${GATLING_VERSION}-bundle.zip
ARG GATLING_BASE=https://repo1.maven.org/maven2/io/gatling/highcharts/gatling-charts-highcharts-bundle/${GATLING_VERSION}

ENV GATLING_HOME=/opt/gatling
ENV PATH="${GATLING_HOME}/bin:${PATH}"

# Install unzip + wget; download bundle; extract; strip the versioned
# directory prefix so /opt/gatling/bin/gatling.sh is a stable path.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends unzip wget ca-certificates; \
    wget -q -O /tmp/gatling.zip "${GATLING_BASE}/${GATLING_BUNDLE}"; \
    mkdir -p "${GATLING_HOME}"; \
    unzip -q /tmp/gatling.zip -d /tmp/gatling; \
    cp -r /tmp/gatling/gatling-charts-highcharts-bundle-${GATLING_VERSION}/* "${GATLING_HOME}"/; \
    rm -rf /tmp/gatling /tmp/gatling.zip; \
    apt-get purge -y wget unzip; \
    apt-get autoremove -y; \
    rm -rf /var/lib/apt/lists/*; \
    chmod +x "${GATLING_HOME}/bin/gatling.sh"

# gatling-run: our exit-code-honest wrapper. Reads assertions.json from
# the run's results dir and exits non-zero on any failed assertion.
# See file header for the "why" — gatling.sh 3.9.x local mode returns
# exit=0 even when assertions fail, defeating the catalog's exit-code
# verdict rule.
COPY gatling-run /usr/local/bin/gatling-run
RUN chmod +x /usr/local/bin/gatling-run

# Convention: mount the user's simulations at /simulations and outputs at
# /results. The catalog template maps /data/repo/<paths> to these mounts
# so the wrapper's artifact glob can find outputs unchanged.
WORKDIR /opt/gatling

# No ENTRYPOINT — the operator's /entry wrapper runs the caller-supplied
# command verbatim. Consumers (config/templates/gatling.yaml) invoke
# /opt/gatling/bin/gatling.sh explicitly.
CMD ["/opt/gatling/bin/gatling.sh", "--help"]
