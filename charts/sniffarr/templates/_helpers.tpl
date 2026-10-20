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
sniffarr.sniffdht is a Deployment of the sniffdht binary, and the Service of its DHT port when
v.service.enabled. It takes root, component, v (the
component's values), discover, fetch, url, existingSecret, secretKey, and secretRequired, the
error when no Secret is set.
*/}}
{{- define "sniffarr.sniffdht" -}}
{{- $root := .root }}
{{- $v := .v }}
{{- $svc := $v.service }}
{{- $port := 42069 }}
{{- if hasKey $v.flags "port" }}
{{- $port = $v.flags.port }}
{{- end }}
{{- $nodePort := 0 }}
{{- if $svc.enabled }}
{{- if not (has $svc.type (list "ClusterIP" "NodePort" "LoadBalancer")) }}
{{- fail (printf "%s.service.type: want ClusterIP, NodePort or LoadBalancer, not %q" .component $svc.type) }}
{{- end }}
{{- if $svc.nodePort }}
{{- if eq $svc.type "ClusterIP" }}
{{- fail (printf "%s.service.nodePort: a ClusterIP Service has no node port" .component) }}
{{- end }}
{{- $nodePort = int $svc.nodePort }}
{{- if and (hasKey $v.flags "port") (ne (int $v.flags.port) $nodePort) }}
{{- fail (printf "%s.flags.port is %v but %s.service.nodePort is %v: the pod listens on the node port, so leave flags.port unset or equal" .component $v.flags.port .component $nodePort) }}
{{- end }}
{{- $port = $nodePort }}
{{- end }}
{{- if not $port }}
{{- fail (printf "%s.service: wants a fixed port, not flags.port 0" .component) }}
{{- end }}
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
            {{- if and $nodePort (not (hasKey $v.flags "port")) }}
            - {{ printf "-port=%d" $nodePort | quote }}
            {{- end }}
            {{- if $v.upnp }}
            - "-upnp"
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
          {{- if and $port (or .discover $v.hostNetwork $svc.enabled) }}
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
{{- if $svc.enabled }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $root.Release.Name }}-{{ .component }}
  labels:
    {{- include "sniffarr.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
  {{- with $svc.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  type: {{ $svc.type }}
  selector:
    {{- include "sniffarr.selector" (dict "root" $root "component" .component) | nindent 4 }}
  ports:
    {{- range $proto := list "tcp" "udp" }}
    - name: dht-{{ $proto }}
      port: {{ $port }}
      targetPort: {{ $port }}
      protocol: {{ upper $proto }}
      {{- with $nodePort }}
      nodePort: {{ . }}
      {{- end }}
    {{- end }}
{{- end }}
{{- end }}
