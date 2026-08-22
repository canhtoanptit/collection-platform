# Helm charts

| Chart | Type | Purpose |
|---|---|---|
| `collections-service` | library | Canonical Kubernetes objects for every collections Go service. Renders nothing on its own. |
| `render-test` | application | **Render test only.** Thin consumer of the library that instantiates every named template. Never deploy it. |

## Consuming the library chart

A service chart (`deployment/charts/<svc>/`) declares the dependency and then
includes one named template per object it needs:

```yaml
# Chart.yaml
dependencies:
  - name: collections-service
    version: 0.1.0
    repository: file://../collections-service
```

```gotemplate
{{/* templates/deployment.yaml */}}
{{ include "collections-service.deployment" . }}
```

Available named templates: `collections-service.deployment`, `.service`,
`.serviceaccount`, `.hpa`, `.cronjobs`, `.migratejob`. Every one is invoked with
the consumer's root context (`.`) and reads the consumer's top-level values —
see `collections-service/values.yaml` for the expected shape.

## Checks

```sh
helm lint deployment/charts/collections-service
helm dependency build deployment/charts/render-test   # vendors the library into charts/
helm lint deployment/charts/render-test
helm template deployment/charts/render-test
```

`helm dependency build` is required after a fresh clone and whenever the library
chart version changes; the vendored `charts/*.tgz` is gitignored, `Chart.lock` is
committed.
