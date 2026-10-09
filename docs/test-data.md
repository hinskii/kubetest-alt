# Test data

How a Test gets the data it runs with — product IDs, users, fixtures, an
`.env` with a test account. Pick by how much there is, how often it
changes and who changes it.

| Data | Where it lives | Use |
|---|---|---|
| A few values, different per run | a parameter | `spec.config` → env or args |
| Versioned with the test | the test's repository | read it as a file of the project |
| Per environment, or secret | a ConfigMap / Secret | `spec.content.testData` |
| Large (tens of MB and up) | an archive URL or a volume | `content.tarball`, a PVC in `spec.pod.volumes` |
| Already in the system | the system itself | fetch it in the test's setup |

## A parameter

Short lists that a run may override from the run form or the CLI:

```yaml
spec:
  use: [k6]
  config:
    productIds: {type: string, default: "1012,1013,2201"}
  container:
    env:
      - {name: PRODUCT_IDS, value: "{{ config.productIds }}"}
```

`__ENV.PRODUCT_IDS.split(',')` in k6, `process.env.PRODUCT_IDS` in
Playwright. `kubectl kubetest run … --config productIds=4001,4002`.

## In the repository

Data that changes with the test sits next to it: `data/products.csv` in
the project, read like any file — with git content the whole checkout is
in `/data/repo`.

## From a ConfigMap or a Secret — `testData`

Data kept apart from the code — different per environment, maintained by
someone else, or secret — is mounted read-only as files. It works with
code from git and with inline code alike.

```yaml
spec:
  content:
    git: {uri: https://github.com/org/shop, revision: main}
    testData:
      - configMap: products-stage            # → /data/testdata/products-stage/<key>
      - configMap: fixtures
        mountPath: /data/repo/e2e/fixtures   # all keys in this directory
      - secret: shop-test-account
        items:                               # chosen keys at exact paths
          - {key: env, path: /data/repo/e2e/.env}
```

- **Default:** every key at `/data/testdata/<name>/<key>`; the test finds
  the directory in `$KUBETEST_TESTDATA_DIR`. Recommended for new tests.
- **`mountPath`:** every key as a file in that directory — so a test that
  already reads `./fixtures/…` keeps working. The directory must be empty
  or new: a mount hides whatever a directory held, so if the checkout has
  files there the run fails at once ("would hide 3 files of the code")
  instead of hiding them.
- **`items`:** single keys at exact paths. This is the only way to replace
  a file of the code, e.g. a config per environment — and it shows in the
  YAML.

The ConfigMap or Secret lives in the Test's namespace. A run fails at once
(`TestDataMissing`) when it — or a key named in `items` — doesn't exist,
instead of hanging on a mount. Secrets are mounted, never passed through
environment variables. Control Center's wizard has a *Test data* section
with the same three choices; names are typed, so the API needs no access
to Secrets.

Not the same as inline files: `content.files` is the test's **code**
typed into the Test, and code comes from one place — git or inline, never
both. Data from the cluster is always `testData`.

## Large data

- **An archive:** `content.tarball` downloads and unpacks it before the
  test starts (e.g. a presigned S3/GCS URL). It is the code source then —
  for code from git plus a large data set, use a volume.
- **A volume:** a PersistentVolumeClaim (ReadWriteMany for data shared
  by many tests) in `spec.pod.volumes`, mounted with
  `spec.container.volumeMounts`.

## In the test's setup

IDs of things that already exist in the system under test are often best
fetched when the test starts — nothing to keep in sync, and a test doesn't
break when a product leaves the catalogue:

```js
// k6
export function setup() {
  const res = http.get(`${__ENV.BASE_URL}/api/products?limit=50`);
  return { ids: res.json().map((p) => p.id) };
}
export default function (data) {
  const id = data.ids[Math.floor(Math.random() * data.ids.length)];
  http.get(`${__ENV.BASE_URL}/products/${id}`);
}
```

Playwright: a `globalSetup` writing them to a file the specs read.
