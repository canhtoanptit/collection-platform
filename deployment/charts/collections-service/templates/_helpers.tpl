{{/*
Shared helpers for the collections-service library chart.

Every template is invoked with the *consumer* chart's root context, so
.Chart / .Release / .Values below refer to the service chart including us.
*/}}

{{/* Short name of the service, from the consumer chart name. */}}
{{- define "collections-service.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified object name: "<release>-<name>", or just the release name
when it already contains the chart name. */}}
{{- define "collections-service.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* chart-version label value. */}}
{{- define "collections-service.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Selector labels: the immutable subset. */}}
{{- define "collections-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "collections-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* Full label set for every rendered object. */}}
{{- define "collections-service.labels" -}}
helm.sh/chart: {{ include "collections-service.chart" . }}
{{ include "collections-service.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: collections
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* ServiceAccount name the pods run as. */}}
{{- define "collections-service.serviceAccountName" -}}
{{- $sa := .Values.serviceAccount | default dict -}}
{{- if dig "create" true $sa -}}
{{- default (include "collections-service.fullname" .) $sa.name -}}
{{- else -}}
{{- default "default" $sa.name -}}
{{- end -}}
{{- end -}}

{{/* "<repository>:<tag>", tag defaulting to the consumer chart appVersion. */}}
{{- define "collections-service.image" -}}
{{- $image := .Values.image | default dict -}}
{{- $repo := required "collections-service: image.repository is required" $image.repository -}}
{{- printf "%s:%s" $repo (default (default "latest" .Chart.AppVersion) $image.tag) -}}
{{- end -}}

{{/* Pod annotations, always including the Prometheus scrape hints. */}}
{{- define "collections-service.podAnnotations" -}}
{{- $out := merge (dict) (.Values.podAnnotations | default dict) (.Values.commonAnnotations | default dict) -}}
{{- $metrics := .Values.metrics | default dict -}}
{{- if dig "enabled" true $metrics -}}
{{- $ports := .Values.containerPorts | default dict -}}
{{- $_ := set $out "prometheus.io/scrape" "true" -}}
{{- $_ := set $out "prometheus.io/port" (printf "%v" (dig "metrics" 9090 $ports)) -}}
{{- $_ := set $out "prometheus.io/path" (dig "path" "/metrics" $metrics) -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/* Hardened pod securityContext, with consumer overrides merged on top. */}}
{{- define "collections-service.podSecurityContext" -}}
{{- $default := dict
      "runAsNonRoot" true
      "runAsUser" 65532
      "runAsGroup" 65532
      "fsGroup" 65532
      "seccompProfile" (dict "type" "RuntimeDefault") -}}
{{- toYaml (mergeOverwrite $default (deepCopy (.Values.podSecurityContext | default dict))) -}}
{{- end -}}

{{/* Hardened container securityContext, with consumer overrides merged on top.
runAsNonRoot / readOnlyRootFilesystem / seccomp RuntimeDefault are the floor. */}}
{{- define "collections-service.containerSecurityContext" -}}
{{- $default := dict
      "runAsNonRoot" true
      "readOnlyRootFilesystem" true
      "allowPrivilegeEscalation" false
      "capabilities" (dict "drop" (list "ALL"))
      "seccompProfile" (dict "type" "RuntimeDefault") -}}
{{- toYaml (mergeOverwrite $default (deepCopy (.Values.securityContext | default dict))) -}}
{{- end -}}

{{/* envFrom entries: the ExternalSecret-synced Secret and optional ConfigMap.
Renders nothing when neither name is set. */}}
{{- define "collections-service.envFrom" -}}
{{- $ef := .Values.envFrom | default dict -}}
{{- with $ef.configMapName }}
- configMapRef:
    name: {{ . }}
    optional: {{ dig "optional" false $ef }}
{{- end }}
{{- with $ef.secretName }}
- secretRef:
    name: {{ . }}
    optional: {{ dig "optional" false $ef }}
{{- end }}
{{- end -}}

{{/* Volumes backing the read-only root filesystem, plus extras. */}}
{{- define "collections-service.volumes" -}}
{{- $tmp := .Values.tmpDir | default dict -}}
{{- if dig "enabled" true $tmp }}
- name: tmp
  emptyDir:
    sizeLimit: {{ dig "sizeLimit" "64Mi" $tmp }}
{{- end }}
{{- with .Values.extraVolumes }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "collections-service.volumeMounts" -}}
{{- $tmp := .Values.tmpDir | default dict -}}
{{- if dig "enabled" true $tmp }}
- name: tmp
  mountPath: /tmp
{{- end }}
{{- with .Values.extraVolumeMounts }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* Scheduling constraints shared by the Deployment, CronJobs and the hook. */}}
{{- define "collections-service.scheduling" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
{{ toYaml . }}
{{- end }}
{{- with .Values.nodeSelector }}
nodeSelector:
{{ toYaml . }}
{{- end }}
{{- with .Values.tolerations }}
tolerations:
{{ toYaml . }}
{{- end }}
{{- with .Values.affinity }}
affinity:
{{ toYaml . }}
{{- end }}
{{- end -}}
