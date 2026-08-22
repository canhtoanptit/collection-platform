{{/*
collections-service.migratejob — goose migrations as a Helm pre-install /
pre-upgrade hook, running `server migrate up` from the service image (plan §2,
"DB access"). Renders nothing when migrate.enabled is false.

The hook is deleted before it is recreated so a re-run of a failed upgrade is
not blocked by a leftover Job object.
*/}}
{{- define "collections-service.migratejob" -}}
{{- $migrate := .Values.migrate | default dict -}}
{{- if dig "enabled" true $migrate -}}
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ printf "%s-migrate" (include "collections-service.fullname" .) | trunc 63 | trimSuffix "-" }}
  labels:
    {{- include "collections-service.labels" . | nindent 4 }}
    collections.internal/job: migrate
  annotations:
    {{- with .Values.commonAnnotations }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": "-5"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
spec:
  backoffLimit: {{ dig "backoffLimit" 1 $migrate }}
  activeDeadlineSeconds: {{ dig "activeDeadlineSeconds" 600 $migrate }}
  ttlSecondsAfterFinished: {{ dig "ttlSecondsAfterFinished" 300 $migrate }}
  template:
    metadata:
      labels:
        {{- include "collections-service.selectorLabels" . | nindent 8 }}
        collections.internal/job: migrate
    spec:
      restartPolicy: Never
      serviceAccountName: {{ include "collections-service.serviceAccountName" . }}
      {{- with (include "collections-service.scheduling" . | trim) }}
      {{- . | nindent 6 }}
      {{- end }}
      securityContext:
        {{- include "collections-service.podSecurityContext" . | nindent 8 }}
      containers:
        - name: migrate
          image: {{ include "collections-service.image" . | quote }}
          imagePullPolicy: {{ dig "pullPolicy" "IfNotPresent" (.Values.image | default dict) }}
          {{- with .Values.command }}
          command:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          args:
            {{- toYaml (dig "args" (list "migrate" "up") $migrate) | nindent 12 }}
          {{- with .Values.env }}
          env:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- with (include "collections-service.envFrom" . | trim) }}
          envFrom:
            {{- . | nindent 12 }}
          {{- end }}
          resources:
            {{- toYaml (dig "resources" (.Values.resources | default dict) $migrate) | nindent 12 }}
          securityContext:
            {{- include "collections-service.containerSecurityContext" . | nindent 12 }}
          {{- with (include "collections-service.volumeMounts" . | trim) }}
          volumeMounts:
            {{- . | nindent 12 }}
          {{- end }}
      {{- with (include "collections-service.volumes" . | trim) }}
      volumes:
        {{- . | nindent 8 }}
      {{- end }}
{{- end -}}
{{- end -}}
