{{/*
collections-service.cronjobs — one CronJob per values.cronJobs[] entry, running
the service image with `server tick <task>` style args (see plan §2, "Scheduled
work"). Entry shape: {name, schedule, args?, env?, resources?, suspend?}.
Renders nothing when the list is empty.
*/}}
{{- define "collections-service.cronjobs" -}}
{{- $root := . -}}
{{- range $job := .Values.cronJobs | default list }}
{{- $name := required "collections-service: cronJobs[].name is required" $job.name }}
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: {{ printf "%s-%s" (include "collections-service.fullname" $root) $name | trunc 52 | trimSuffix "-" }}
  labels:
    {{- include "collections-service.labels" $root | nindent 4 }}
    collections.internal/tick: {{ $name }}
  {{- with $root.Values.commonAnnotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  schedule: {{ required "collections-service: cronJobs[].schedule is required" $job.schedule | quote }}
  timeZone: {{ dig "timeZone" "Etc/UTC" $job | quote }}
  suspend: {{ dig "suspend" false $job }}
  concurrencyPolicy: {{ dig "concurrencyPolicy" "Forbid" $job }}
  startingDeadlineSeconds: {{ dig "startingDeadlineSeconds" 300 $job }}
  successfulJobsHistoryLimit: {{ dig "successfulJobsHistoryLimit" 3 $job }}
  failedJobsHistoryLimit: {{ dig "failedJobsHistoryLimit" 3 $job }}
  jobTemplate:
    spec:
      backoffLimit: {{ dig "backoffLimit" 1 $job }}
      {{- with dig "activeDeadlineSeconds" 3600 $job }}
      activeDeadlineSeconds: {{ . }}
      {{- end }}
      template:
        metadata:
          labels:
            {{- include "collections-service.selectorLabels" $root | nindent 12 }}
            collections.internal/tick: {{ $name }}
        spec:
          restartPolicy: Never
          serviceAccountName: {{ include "collections-service.serviceAccountName" $root }}
          {{- with (include "collections-service.scheduling" $root | trim) }}
          {{- . | nindent 10 }}
          {{- end }}
          securityContext:
            {{- include "collections-service.podSecurityContext" $root | nindent 12 }}
          containers:
            - name: tick
              image: {{ include "collections-service.image" $root | quote }}
              imagePullPolicy: {{ dig "pullPolicy" "IfNotPresent" ($root.Values.image | default dict) }}
              {{- with $root.Values.command }}
              command:
                {{- toYaml . | nindent 16 }}
              {{- end }}
              args:
                {{- toYaml (dig "args" (list "tick" $name) $job) | nindent 16 }}
              {{- with concat ($root.Values.env | default list) (dig "env" list $job) }}
              env:
                {{- toYaml . | nindent 16 }}
              {{- end }}
              {{- with (include "collections-service.envFrom" $root | trim) }}
              envFrom:
                {{- . | nindent 16 }}
              {{- end }}
              resources:
                {{- toYaml (dig "resources" ($root.Values.resources | default dict) $job) | nindent 16 }}
              securityContext:
                {{- include "collections-service.containerSecurityContext" $root | nindent 16 }}
              {{- with (include "collections-service.volumeMounts" $root | trim) }}
              volumeMounts:
                {{- . | nindent 16 }}
              {{- end }}
          {{- with (include "collections-service.volumes" $root | trim) }}
          volumes:
            {{- . | nindent 12 }}
          {{- end }}
{{- end }}
{{- end -}}
