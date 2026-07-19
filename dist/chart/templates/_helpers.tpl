{{/*
Expand the name of the chart.
*/}}
{{- define "gratefulagents.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "gratefulagents.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Namespace for generated references.
Always uses the Helm release namespace.
*/}}
{{- define "gratefulagents.namespaceName" -}}
{{- .Release.Namespace }}
{{- end }}

{{/*
Resource name with proper truncation for Kubernetes 63-character limit.
Takes a dict with:
  - .suffix: Resource name suffix (e.g., "metrics", "webhook")
  - .context: Template context (root context with .Values, .Release, etc.)
Dynamically calculates safe truncation to ensure total name length <= 63 chars.
*/}}
{{- define "gratefulagents.resourceName" -}}
{{- $fullname := include "gratefulagents.fullname" .context }}
{{- $suffix := .suffix }}
{{- $maxLen := sub 62 (len $suffix) | int }}
{{- if gt (len $fullname) $maxLen }}
{{- printf "%s-%s" (trunc $maxLen $fullname | trimSuffix "-") $suffix | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" $fullname $suffix | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
PostgreSQL connection URL: explicit database.url, otherwise the bundled
Postgres. Fails template rendering when neither is configured.
*/}}
{{- define "gratefulagents.databaseURL" -}}
{{- if .Values.database.url }}
{{- .Values.database.url }}
{{- else if .Values.postgres.enabled }}
{{- $host := include "gratefulagents.resourceName" (dict "suffix" "postgres" "context" $) }}
{{- printf "postgres://%s:%s@%s.%s.svc:5432/%s?sslmode=disable" (.Values.postgres.auth.username | urlquery) (.Values.postgres.auth.password | urlquery) $host .Release.Namespace .Values.postgres.auth.database }}
{{- else }}
{{- fail "PostgreSQL is required: set database.url or enable the bundled postgres (postgres.enabled=true)" }}
{{- end }}
{{- end }}

{{/*
OTLP trace endpoint (host:port): explicit tracing.otlpEndpoint, otherwise the
bundled Jaeger. Empty when tracing is not configured.
*/}}
{{- define "gratefulagents.otlpEndpoint" -}}
{{- if .Values.tracing.otlpEndpoint }}
{{- .Values.tracing.otlpEndpoint }}
{{- else if .Values.jaeger.enabled }}
{{- printf "%s.%s.svc:4317" (include "gratefulagents.resourceName" (dict "suffix" "jaeger" "context" $)) .Release.Namespace }}
{{- end }}
{{- end }}

{{/*
S3-compatible storage settings: explicit objectStorage.*, otherwise the
bundled MinIO. Endpoint empty when object storage is not configured.
*/}}
{{- define "gratefulagents.s3Endpoint" -}}
{{- if .Values.objectStorage.endpoint }}
{{- .Values.objectStorage.endpoint }}
{{- else if .Values.minio.enabled }}
{{- printf "http://%s.%s.svc:9000" (include "gratefulagents.resourceName" (dict "suffix" "minio" "context" $)) .Release.Namespace }}
{{- end }}
{{- end }}

{{- define "gratefulagents.s3Bucket" -}}
{{- if .Values.objectStorage.endpoint }}
{{- .Values.objectStorage.bucket }}
{{- else if .Values.minio.enabled }}
{{- .Values.minio.bucket }}
{{- end }}
{{- end }}

{{- define "gratefulagents.s3Region" -}}
{{- if .Values.objectStorage.endpoint }}
{{- .Values.objectStorage.region }}
{{- else if .Values.minio.enabled }}
{{- .Values.minio.region }}
{{- end }}
{{- end }}

{{- define "gratefulagents.s3AccessKeyID" -}}
{{- if .Values.objectStorage.endpoint }}
{{- .Values.objectStorage.accessKeyID }}
{{- else if .Values.minio.enabled }}
{{- .Values.minio.rootUser }}
{{- end }}
{{- end }}

{{- define "gratefulagents.s3SecretAccessKey" -}}
{{- if .Values.objectStorage.endpoint }}
{{- .Values.objectStorage.secretAccessKey }}
{{- else if .Values.minio.enabled }}
{{- .Values.minio.rootPassword }}
{{- end }}
{{- end }}
