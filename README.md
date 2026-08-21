# kubetest-alt

In-house Kubernetes-native test execution platform. Operator +
API server + CRDs (`Test`, `TestRun`, `TestTemplate`, `TestTrigger`,
`Webhook`), a curated 15-template tool catalog (k6, cypress, jmeter,
gatling, playwright, pytest, gradle, maven, ...), cron + Kubernetes
event triggers, MinIO artifact + log storage, Postgres run history,
outbound webhooks, and Prometheus metrics — packaged as a single Helm
chart.

Alternative to Testkube's OSS agent with the dashboard Testkube keeps
behind their paid Control Plane. See [CLAUDE.md](CLAUDE.md) for the
full architecture rationale.

## Quickstart

Zero-to-first-run in 5 commands (kind + helm + kubectl on PATH):

```sh
# 1. Cluster
kind create cluster --name kubetest-alt

# 2. Chart install (uses the chart's helm-generated self-signed webhook
#    certs; no cert-manager dependency for dev)
helm install kt charts/kubetest-alt/ --namespace kubetest-alt --create-namespace --wait

# 3. Apply the catalog (15 TestTemplates + one sample Test each)
kubectl apply -k config/samples/tools/

# 4. Fire a run — creates a TestRun against the sample-k6 Test
kubectl create -f - <<EOF
apiVersion: tests.kubetest.io/v1alpha1
kind: TestRun
metadata: { generateName: quickstart-, namespace: default }
spec: { testRef: sample-k6, source: cli }
EOF

# 5. Watch it complete
kubectl get testruns -w
```

Optional add-ons (each independent — install only what you use):

- **MinIO** for artifacts + logs: `helm install minio oci://registry-1.docker.io/bitnamicharts/minio ...`
- **Postgres** for run history: `helm install postgres oci://registry-1.docker.io/bitnamicharts/postgresql ...`

Then rerun `helm upgrade kt charts/kubetest-alt/ -f your-values.yaml`.

## Prerequisites
- Go 1.26+
- Docker 17.03+
- kubectl 1.29+
- Kubernetes 1.29+ cluster (kind, k3s, EKS, GKE, ...)

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/kubetest-alt:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/kubetest-alt:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following the options to release and provide this solution to the users.

### By providing a bundle with all YAML files

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/kubetest-alt:tag
```

**NOTE:** The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without its
dependencies.

2. Using the installer

Users can just run 'kubectl apply -f <URL for YAML BUNDLE>' to install
the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/kubetest-alt/<tag or branch>/dist/install.yaml
```

### By providing a Helm Chart

1. Build the chart using the optional helm plugin

```sh
kubebuilder edit --plugins=helm/v2-alpha
```

2. See that a chart was generated under 'dist/chart', and users
can obtain this solution from there.

**NOTE:** If you change the project, you need to update the Helm Chart
using the same command above to sync the latest changes. Furthermore,
if you create webhooks, you need to use the above command with
the '--force' flag and manually ensure that any custom configuration
previously added to 'dist/chart/values.yaml' or 'dist/chart/manager/manager.yaml'
is manually re-applied afterwards.

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

