{{- define "svc.labels" -}}
app.kubernetes.io/name: {{ .Values.name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: {{ .Values.name }}
app.kubernetes.io/part-of: food-platform
app.kubernetes.io/version: {{ .Values.image.tag | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end }}

{{- define "svc.selectorLabels" -}}
app.kubernetes.io/name: {{ .Values.name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "svc.image" -}}
{{ required "image.repository is required" .Values.image.repository }}:{{ .Values.image.tag }}
{{- end }}

{{/* Контейнерные переменные из values.env — для Job-миграций (ConfigMap-хук до создания чарта не доступен) */}}
{{- define "svc.inlineEnv" -}}
{{- range $k, $v := .Values.env }}
- name: {{ $k }}
  value: {{ $v | toString | quote }}
{{- end }}
{{- end }}
