{{- define "sniffarr.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "sniffarr.selector" -}}
app.kubernetes.io/name: {{ .root.Chart.Name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end }}

{{- define "sniffarr.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{- define "sniffarr.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{- define "sniffarr.securityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end }}

{{- define "sniffarr.env" -}}
{{- if not .root.Values.telemetry.enabled }}
- name: OTEL_SDK_DISABLED
  value: "true"
{{- end }}
{{- range $k, $v := .env }}
- name: {{ $k }}
  value: {{ $v | toString | quote }}
{{- end }}
{{- end }}

{{- /*
sniffarr.sniffdht is a Deployment of the sniffdht binary. It takes root, component, v (the
component's values), discover, fetch, url, existingSecret, secretKey, and secretRequired, the
error when no Secret is set.
*/}}
{{- define "sniffarr.sniffdht" -}}
{{- $root := .root }}
{{- $v := .v }}
{{- $port := 42069 }}
{{- if hasKey $v.flags "port" }}
{{- $port = $v.flags.port }}
{{- end }}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $root.Release.Name }}-{{ .component }}
  labels:
    {{- include "sniffarr.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
spec:
  replicas: {{ $v.replicas }}
  {{- if $v.hostNetwork }}
  strategy:
    type: Recreate
  {{- end }}
  selector:
    matchLabels:
      {{- include "sniffarr.selector" (dict "root" $root "component" .component) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "sniffarr.selector" (dict "root" $root "component" .component) | nindent 8 }}
        {{- with $v.podLabels }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      {{- with $v.podAnnotations }}
      annotations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
    spec:
      {{- with $root.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- if $v.hostNetwork }}
      hostNetwork: true
      dnsPolicy: ClusterFirstWithHostNet
      {{- end }}
      securityContext:
        {{- include "sniffarr.podSecurityContext" $root | nindent 8 }}
      containers:
        - name: sniffdht
          image: {{ include "sniffarr.image" $root }}
          imagePullPolicy: {{ $root.Values.image.pullPolicy }}
          command: [/sniffdht]
          args:
            - {{ printf "-discover=%v" .discover | quote }}
            - {{ printf "-fetch=%v" .fetch | quote }}
            {{- range $k, $f := $v.flags }}
            - {{ printf "-%s=%v" $k $f | quote }}
            {{- end }}
          env:
            - name: SNIFFARR_URL
              value: {{ .url | default (printf "http://%s-api:8080" $root.Release.Name) | quote }}
            - name: SNIFFARR_INGEST_TOKEN
              valueFrom:
                secretKeyRef:
                  name: {{ required .secretRequired (.existingSecret | default $root.Values.api.existingSecret) }}
                  key: {{ .secretKey }}
            {{- with include "sniffarr.env" (dict "root" $root "env" $v.env) | trim }}
            {{- . | nindent 12 }}
            {{- end }}
          {{- if and $port (or .discover $v.hostNetwork) }}
          ports:
            - name: dht
              containerPort: {{ $port }}
              protocol: UDP
            - name: peers
              containerPort: {{ $port }}
              protocol: TCP
          {{- end }}
          {{- with $v.resources }}
          resources:
            {{- toYaml . | nindent 12 }}
          {{- end }}
          securityContext:
            {{- include "sniffarr.securityContext" $root | nindent 12 }}
          volumeMounts:
            - name: tmp
              mountPath: /tmp
      volumes:
        - name: tmp
          emptyDir: {}
      {{- with $v.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $v.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $v.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
{{- end }}
