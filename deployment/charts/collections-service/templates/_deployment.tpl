{{/*
collections-service.deployment — the service Deployment.

Usage from a consumer chart's templates/deployment.yaml:
  {{ include "collections-service.deployment" . }}
*/}}
{{- define "collections-service.deployment" -}}
{{- $ports := .Values.containerPorts | default dict -}}
{{- $probes := .Values.probes | default dict -}}
{{- $readiness := dig "readiness" dict $probes -}}
{{- $liveness := dig "liveness" dict $probes -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "collections-service.fullname" . }}
  labels:
    {{- include "collections-service.labels" . | nindent 4 }}
  {{- with .Values.commonAnnotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  {{- if not (dig "enabled" false (.Values.autoscaling | default dict)) }}
  replicas: {{ .Values.replicaCount | default 2 }}
  {{- end }}
  revisionHistoryLimit: 3
  selector:
    matchLabels:
      {{- include "collections-service.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      annotations:
        {{- include "collections-service.podAnnotations" . | nindent 8 }}
      labels:
        {{- include "collections-service.selectorLabels" . | nindent 8 }}
        {{- with .Values.podLabels }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
    spec:
      serviceAccountName: {{ include "collections-service.serviceAccountName" . }}
      {{- with (include "collections-service.scheduling" . | trim) }}
      {{- . | nindent 6 }}
      {{- end }}
      securityContext:
        {{- include "collections-service.podSecurityContext" . | nindent 8 }}
      terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds | default 30 }}
      {{- with .Values.topologySpreadConstraints }}
      topologySpreadConstraints:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      containers:
        - name: {{ include "collections-service.name" . }}
          image: {{ include "collections-service.image" . | quote }}
          imagePullPolicy: {{ dig "pullPolicy" "IfNotPresent" (.Values.image | default dict) }}
          {{- with .Values.command }}
          command:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          args:
            {{- toYaml (.Values.args | default (list "serve")) | nindent 12 }}
          ports:
            - name: http
              containerPort: {{ dig "http" 8080 $ports }}
              protocol: TCP
            - name: metrics
              containerPort: {{ dig "metrics" 9090 $ports }}
              protocol: TCP
          {{- with .Values.env }}
          env:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- with (include "collections-service.envFrom" . | trim) }}
          envFrom:
            {{- . | nindent 12 }}
          {{- end }}
          readinessProbe:
            httpGet:
              path: {{ dig "path" "/readyz" $readiness }}
              port: http
            initialDelaySeconds: {{ dig "initialDelaySeconds" 5 $readiness }}
            periodSeconds: {{ dig "periodSeconds" 10 $readiness }}
            timeoutSeconds: {{ dig "timeoutSeconds" 2 $readiness }}
            failureThreshold: {{ dig "failureThreshold" 3 $readiness }}
          livenessProbe:
            httpGet:
              path: {{ dig "path" "/healthz" $liveness }}
              port: http
            initialDelaySeconds: {{ dig "initialDelaySeconds" 15 $liveness }}
            periodSeconds: {{ dig "periodSeconds" 20 $liveness }}
            timeoutSeconds: {{ dig "timeoutSeconds" 2 $liveness }}
            failureThreshold: {{ dig "failureThreshold" 3 $liveness }}
          resources:
            {{- toYaml (.Values.resources | default dict) | nindent 12 }}
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
