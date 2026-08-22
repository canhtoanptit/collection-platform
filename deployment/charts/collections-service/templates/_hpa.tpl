{{/*
collections-service.hpa — HorizontalPodAutoscaler.
Renders nothing when autoscaling.enabled is false (the default); the Deployment
then keeps its static replicas field.
*/}}
{{- define "collections-service.hpa" -}}
{{- $hpa := .Values.autoscaling | default dict -}}
{{- if dig "enabled" false $hpa -}}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ include "collections-service.fullname" . }}
  labels:
    {{- include "collections-service.labels" . | nindent 4 }}
  {{- with .Values.commonAnnotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {{ include "collections-service.fullname" . }}
  minReplicas: {{ dig "minReplicas" 2 $hpa }}
  maxReplicas: {{ dig "maxReplicas" 6 $hpa }}
  metrics:
    {{- with dig "targetCPUUtilizationPercentage" 70 $hpa }}
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: {{ . }}
    {{- end }}
    {{- with dig "targetMemoryUtilizationPercentage" "" $hpa }}
    - type: Resource
      resource:
        name: memory
        target:
          type: Utilization
          averageUtilization: {{ . }}
    {{- end }}
{{- end -}}
{{- end -}}
