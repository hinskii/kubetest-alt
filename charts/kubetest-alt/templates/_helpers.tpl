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

{{/* Object-storage flags, shared by the operator and the API server so
they always point at the same backend and bucket. */}}
{{- define "kubetest-alt.storageArgs" -}}
{{- $s := .Values.storage -}}
{{- if not (has $s.type (list "" "s3" "gcs")) -}}
{{- fail (printf "storage.type must be \"s3\", \"gcs\" or empty, got %q" $s.type) -}}
{{- end -}}
{{- if $s.type }}
- --storage-type={{ $s.type }}
- --storage-bucket={{ required "storage.bucket is required when storage.type is set" $s.bucket }}
{{- if eq $s.type "s3" }}
{{- with $s.s3.endpoint }}
- --s3-endpoint={{ . }}
{{- end }}
{{- if $s.s3.useSSL }}
- --s3-use-ssl
{{- end }}
{{- with $s.s3.region }}
- --s3-region={{ . }}
{{- end }}
{{- end }}
{{- if and (eq $s.type "gcs") $s.gcs.endpoint }}
- --gcs-endpoint={{ $s.gcs.endpoint }}
{{- end }}
{{- end }}
{{- end -}}

{{/* S3 static credentials for the operator's / API server's own client,
from storage.s3.secretName. Nothing for GCS (service account) or when no
Secret is set (AWS credential chain, e.g. IRSA). */}}
{{- define "kubetest-alt.storageEnv" -}}
{{- $s := .Values.storage -}}
{{- if and (eq $s.type "s3") $s.s3.secretName }}
- name: AWS_ACCESS_KEY_ID
  valueFrom:
    secretKeyRef:
      name: {{ $s.s3.secretName }}
      key: AWS_ACCESS_KEY_ID
- name: AWS_SECRET_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ $s.s3.secretName }}
      key: AWS_SECRET_ACCESS_KEY
{{- end }}
{{- end -}}

{{/* Control Center (step 18h). */}}
{{- define "kubetest-alt.controlCenter.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: control-center
{{- end -}}

{{- define "kubetest-alt.controlCenter.name" -}}
{{ include "kubetest-alt.fullname" . }}-control-center
{{- end -}}

{{/* Everyone the sign-in proxy lets in by address: allowedEmails plus
     the rbac lists, lower-cased, de-duplicated, sorted. */}}
{{- define "kubetest-alt.controlCenter.emails" -}}
{{- $cc := .Values.controlCenter -}}
{{- $all := concat $cc.auth.google.allowedEmails $cc.rbac.admins $cc.rbac.developers -}}
{{- $out := list -}}
{{- range $all }}{{ $out = append $out (lower (trim .)) }}{{ end -}}
{{- $out | uniq | sortAlpha | join "\n" -}}
{{- end -}}
