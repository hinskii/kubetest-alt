{{/*
Chart-wide helpers. Kept SHORT — the goal is name/label consistency,
not a mini-templating language on top of helm.
*/}}

{{/* Fully-qualified name — release name + chart name, truncated to
     the 63-char DNS label limit. Common Bitnami-style pattern. */}}
{{- define "kubetest-alt.fullname" -}}
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

{{/* Common labels applied to every rendered object. */}}
{{- define "kubetest-alt.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/* Selector labels — subset of the above; NEVER change these across
     chart versions (immutable Selector on Deployments). */}}
{{- define "kubetest-alt.operator.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: operator
{{- end -}}

{{- define "kubetest-alt.apiserver.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: apiserver
{{- end -}}

{{/* Image reference resolver. Argument is a values.images.<key> map
     (or nested map). Applies:
       - images.registry prefix (if set)
       - falls back to chart.AppVersion when tag is empty. */}}
{{- define "kubetest-alt.image" -}}
{{- $img := index .top .key -}}
{{- $registry := .top.registry -}}
{{- $tag := $img.tag | default .ctx.Chart.AppVersion -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $img.repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $img.repository $tag -}}
{{- end -}}
{{- end -}}

{{/* Names of the two ServiceAccounts. Deliberately distinct so the
     apiserver's Role (get on secrets etc.) doesn't creep into the
     operator's — see chart's separate ClusterRoles. */}}
{{- define "kubetest-alt.operator.serviceAccountName" -}}
{{ include "kubetest-alt.fullname" . }}-operator
{{- end -}}

{{- define "kubetest-alt.apiserver.serviceAccountName" -}}
{{ include "kubetest-alt.fullname" . }}-apiserver
{{- end -}}
