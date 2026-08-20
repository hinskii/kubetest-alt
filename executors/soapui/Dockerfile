# kubetest-alt platform image — SoapUI OSS test runner.
#
# Rationale (mini-15B closing): the session-15B template originally
# pointed at kubeshop/testkube-soapui-executor:2.1.123 — coupling to
# another test platform's vendored image is a smell. SoapUI OSS 5.7.2
# is the last public release (SmartBear now pushes Ready! API) and
# the linux bundle tar.gz is downloadable from eviware.com.
#
# Bundling ourselves:
#   * eclipse-temurin:11-jre base (SoapUI 5.7.2 targets JRE 11+)
#   * Pin SoapUI 5.7.2 — the last OSS release
#   * Strip the versioned directory prefix so /opt/soapui/bin/... is
#     a stable path
#
# Smoke verified: pass=0 / fail=255 on kubetest-alt-soapui:5.7.2
# (arm64 native via JVM) — trivial groovy assertion project.

FROM eclipse-temurin:11-jre

ARG SOAPUI_VERSION=5.7.2

ENV SOAPUI_HOME=/opt/soapui
ENV PATH="${SOAPUI_HOME}/bin:${PATH}"

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends wget ca-certificates; \
    wget -q -O /tmp/soapui.tgz \
        "https://dl.eviware.com/soapuios/${SOAPUI_VERSION}/SoapUI-${SOAPUI_VERSION}-linux-bin.tar.gz"; \
    mkdir -p "${SOAPUI_HOME}"; \
    tar -xzf /tmp/soapui.tgz -C /tmp; \
    cp -r "/tmp/SoapUI-${SOAPUI_VERSION}/"* "${SOAPUI_HOME}/"; \
    rm -rf /tmp/soapui.tgz "/tmp/SoapUI-${SOAPUI_VERSION}"; \
    apt-get purge -y wget; \
    apt-get autoremove -y; \
    rm -rf /var/lib/apt/lists/*; \
    chmod +x "${SOAPUI_HOME}/bin/testrunner.sh" \
             "${SOAPUI_HOME}/bin/loadtestrunner.sh" \
             "${SOAPUI_HOME}/bin/mockservicerunner.sh" \
             "${SOAPUI_HOME}/bin/securitytestrunner.sh"

WORKDIR /opt/soapui

# No ENTRYPOINT — the operator's /entry wrapper runs the caller-
# supplied command. Consumers invoke testrunner.sh (or one of the
# other *runner.sh scripts) from the catalog template.
CMD ["testrunner.sh"]
