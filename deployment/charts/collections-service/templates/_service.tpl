{{/*
collections-service.service — ClusterIP Service exposing http + metrics.
There is no Ingress before Phase 12; access is via `kubectl port-forward`.
*/}}
{{- define "collections-service.service" -}}
{{- $svc := .Values.service | default dict -}}
{{- $svcPorts := dig "ports" dict $svc -}}
{{- $ports := .Values.containerPorts | default dict -}}
apiVersion: v1
kind: Service
metadata:
  name: {{ include "collections-service.fullname" . }}
  labels:
    {{- include "collections-service.labels" . | nindent 4 }}
  {{- $annotations := merge (dict) (dig "annotations" dict $svc) (.Values.commonAnnotations | default dict) }}
  {{- with $annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  type: {{ dig "type" "ClusterIP" $svc }}
  ports:
    - name: http
      port: {{ dig "http" 8080 $svcPorts }}
      targetPort: http
      protocol: TCP
    - name: metrics
      port: {{ dig "metrics" 9090 $svcPorts }}
      targetPort: metrics
      protocol: TCP
  selector:
    {{- include "collections-service.selectorLabels" . | nindent 4 }}
{{- end -}}
