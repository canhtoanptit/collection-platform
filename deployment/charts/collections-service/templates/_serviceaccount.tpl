{{/*
collections-service.serviceaccount — ServiceAccount with optional IRSA binding.
Renders nothing when serviceAccount.create is false.
*/}}
{{- define "collections-service.serviceaccount" -}}
{{- $sa := .Values.serviceAccount | default dict -}}
{{- if dig "create" true $sa -}}
{{- $annotations := merge (dict) (dig "annotations" dict $sa) (.Values.commonAnnotations | default dict) -}}
{{- with $sa.roleArn -}}
{{- $_ := set $annotations "eks.amazonaws.com/role-arn" . -}}
{{- end -}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "collections-service.serviceAccountName" . }}
  labels:
    {{- include "collections-service.labels" . | nindent 4 }}
  {{- with $annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
automountServiceAccountToken: true
{{- end -}}
{{- end -}}
