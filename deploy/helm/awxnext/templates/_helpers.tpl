{{- define "awxnext.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "awxnext.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "awxnext.selector" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "awxnext.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{- define "awxnext.serviceAccount" -}}
{{- if .Values.serviceAccount.create -}}
{{ .Values.serviceAccount.name | default (include "awxnext.fullname" .) }}
{{- else -}}
{{ .Values.serviceAccount.name | default "default" }}
{{- end -}}
{{- end -}}

{{/* The generated secret: master key, admin password, bundled database. */}}
{{- define "awxnext.secret" -}}
{{ include "awxnext.fullname" . }}-secrets
{{- end -}}

{{- define "awxnext.serviceURL" -}}
http://{{ include "awxnext.fullname" . }}.{{ .Release.Namespace }}.svc:{{ .Values.service.port }}
{{- end -}}

{{/* The environment of awxnext's containers: database, keys, and this chart's settings. */}}
{{- define "awxnext.env" -}}
- name: AWXNEXT_DATABASE_URL
  valueFrom:
    secretKeyRef:
      {{- if .Values.postgresql.bundled }}
      name: {{ include "awxnext.secret" . }}
      key: database-url
      {{- else }}
      name: {{ required "postgresql.external.existingSecret is required without the bundled PostgreSQL" .Values.postgresql.external.existingSecret }}
      key: url
      {{- end }}
- name: AWXNEXT_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.secretKey.existingSecret | default (include "awxnext.secret" .) }}
      key: secret-key
- name: AWXNEXT_CONTAINER_RUNTIME
  value: {{ .Values.containerRuntime | quote }}
- name: AWXNEXT_DEFAULT_EE_IMAGE
  value: {{ .Values.defaultExecutionEnvironment | quote }}
- name: AWXNEXT_SHIM_IMAGE
  value: {{ include "awxnext.image" . | quote }}
- name: AWXNEXT_SERVICE_URL
  value: {{ include "awxnext.serviceURL" . | quote }}
- name: AWXNEXT_BASE_URL
  {{- if .Values.ingress.enabled }}
  value: {{ printf "%s://%s" (ternary "https" "http" (gt (len .Values.ingress.tls) 0)) .Values.ingress.host | quote }}
  {{- else }}
  value: {{ include "awxnext.serviceURL" . | quote }}
  {{- end }}
{{- with .Values.env }}
{{ toYaml . }}
{{- end }}
{{- end -}}
