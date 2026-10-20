{{- define "sniffarr.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- /*
sniffarr.selector selects the pods of component; with single.enabled it selects the one pod of
the release, whatever component is given.
*/}}
{{- define "sniffarr.selector" -}}
app.kubernetes.io/name: {{ .root.Chart.Name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- if not .root.Values.single.enabled }}
app.kubernetes.io/component: {{ .component }}
{{- end }}
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

{{- /* sniffarr.uiLoopback is "true" when the UI address is reachable only from its own pod. */}}
{{- define "sniffarr.uiLoopback" -}}
{{- if or (hasPrefix "127." .) (hasPrefix "localhost" .) (hasPrefix "[::1]" .) }}true{{- end }}
{{- end }}

{{- /* sniffarr.uiPort is the container port of the UI address. */}}
{{- define "sniffarr.uiPort" -}}
{{ splitList ":" . | last | atoi }}
{{- end }}

{{- /*
sniffarr.dhtPort is the DHT port of a component, as YAML with port and nodePort, nodePort 0
unless its Service fixes one. It takes component, flags and service, and fails on a Service
over a port the cluster picks or a node port flags.port disagrees with.
*/}}
{{- define "sniffarr.dhtPort" -}}
{{- $port := 42069 }}
{{- if hasKey .flags "port" }}
{{- $port = .flags.port }}
{{- end }}
{{- $nodePort := 0 }}
{{- $svc := .service }}
{{- if $svc.enabled }}
{{- if not (has $svc.type (list "ClusterIP" "NodePort" "LoadBalancer")) }}
{{- fail (printf "%s.service.type: want ClusterIP, NodePort or LoadBalancer, not %q" .component $svc.type) }}
{{- end }}
{{- if $svc.nodePort }}
{{- if eq $svc.type "ClusterIP" }}
{{- fail (printf "%s.service.nodePort: a ClusterIP Service has no node port" .component) }}
{{- end }}
{{- $nodePort = int $svc.nodePort }}
{{- if and (hasKey .flags "port") (ne (int .flags.port) $nodePort) }}
{{- fail (printf "%s.flags.port is %v but %s.service.nodePort is %v: the pod listens on the node port, so leave flags.port unset or equal" .component .flags.port .component $nodePort) }}
{{- end }}
{{- $port = $nodePort }}
{{- end }}
{{- if not $port }}
{{- fail (printf "%s.service: wants a fixed port, not flags.port 0" .component) }}
{{- end }}
{{- end }}
port: {{ $port }}
nodePort: {{ $nodePort }}
{{- end }}

{{- /* sniffarr.dhtArgs renders the DHT flags: flags, -port from a node port, and -upnp. */}}
{{- define "sniffarr.dhtArgs" -}}
{{- range $k, $f := .flags }}
- {{ printf "-%s=%v" $k $f | quote }}
{{- end }}
{{- if and .nodePort (not (hasKey .flags "port")) }}
- {{ printf "-port=%d" .nodePort | quote }}
{{- end }}
{{- if .upnp }}
- "-upnp"
{{- end }}
{{- end }}

{{- define "sniffarr.dhtContainerPorts" -}}
- name: dht
  containerPort: {{ .port }}
  protocol: UDP
- name: peers
  containerPort: {{ .port }}
  protocol: TCP
{{- end }}

{{- /*
sniffarr.dhtService is the Service of a DHT port, TCP and UDP. It takes root, name, component
(the pods it selects), service, port and nodePort.
*/}}
{{- define "sniffarr.dhtService" -}}
{{- $svc := .service }}
apiVersion: v1
kind: Service
metadata:
  name: {{ .name }}
  labels:
    {{- include "sniffarr.labels" .root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
  {{- with $svc.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  type: {{ $svc.type }}
  selector:
    {{- include "sniffarr.selector" (dict "root" .root "component" .component) | nindent 4 }}
  ports:
    {{- range $proto := list "tcp" "udp" }}
    - name: dht-{{ $proto }}
      port: {{ $.port }}
      targetPort: {{ $.port }}
      protocol: {{ upper $proto }}
      {{- with $.nodePort }}
      nodePort: {{ . }}
      {{- end }}
    {{- end }}
{{- end }}

{{- /*
sniffarr.litestreamInitContainers restores /data/sniffarr.db before the pod starts and
replicates it, as a sidecar, while it runs. It takes root and litestream, the api.litestream
values.
*/}}
{{- define "sniffarr.litestreamInitContainers" -}}
{{- $ls := .litestream }}
{{- range list "restore" "replicate" }}
- name: {{ . }}
  image: {{ $ls.image.repository }}:{{ $ls.image.tag }}
  imagePullPolicy: {{ $ls.image.pullPolicy }}
  {{- if eq . "restore" }}
  args: [restore, -if-db-not-exists, -if-replica-exists, /data/sniffarr.db]
  {{- else }}
  args: [replicate]
  restartPolicy: Always
  {{- end }}
  {{- with $ls.existingSecret }}
  envFrom:
    - secretRef:
        name: {{ . }}
  {{- end }}
  {{- with $ls.resources }}
  resources:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  securityContext:
    {{- include "sniffarr.securityContext" $.root | nindent 4 }}
  volumeMounts:
    - name: data
      mountPath: /data
    - name: tmp
      mountPath: /tmp
    - name: litestream-config
      mountPath: /etc/litestream.yml
      subPath: litestream.yml
{{- end }}
{{- end }}

{{- /*
sniffarr.dataVolumes are the volumes of a pod running -api: tmp, data per api.persistence and
the litestream ConfigMap when enabled. It takes root and claim, the name of the claim the chart
creates.
*/}}
{{- define "sniffarr.dataVolumes" -}}
{{- $p := .root.Values.api.persistence }}
{{- $ls := .root.Values.api.litestream }}
{{- if not (has $p.type (list "emptyDir" "hostPath" "pvc")) }}
{{- fail (printf "api.persistence.type: want emptyDir, hostPath or pvc, not %q" $p.type) }}
{{- end }}
- name: tmp
  emptyDir: {}
- name: data
  {{- if eq $p.type "hostPath" }}
  hostPath:
    path: {{ required "api.persistence.hostPath.path is required for type hostPath" $p.hostPath.path }}
    type: DirectoryOrCreate
  {{- else if eq $p.type "pvc" }}
  persistentVolumeClaim:
    claimName: {{ $p.existingClaim | default .claim }}
  {{- else }}
  emptyDir: {}
  {{- end }}
{{- if $ls.enabled }}
- name: litestream-config
  configMap:
    name: {{ required "api.litestream.existingConfigMap is required when litestream is enabled" $ls.existingConfigMap }}
{{- end }}
{{- end }}

{{- /*
sniffarr.dht is a Deployment of sniffarr with its DHT components, and the Service of its DHT
port when v.service.enabled. It takes root, component, v (the component's values), sniff and
fetch (the flags), url, existingSecret, secretKey, and secretRequired, the error when no Secret
is set.
*/}}
{{- define "sniffarr.dht" -}}
{{- $root := .root }}
{{- $v := .v }}
{{- $svc := $v.service }}
{{- $dht := include "sniffarr.dhtPort" (dict "component" .component "flags" $v.flags "service" $svc) | fromYaml }}
{{- $port := int $dht.port }}
{{- $nodePort := int $dht.nodePort }}
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
        - name: {{ .component }}
          image: {{ include "sniffarr.image" $root }}
          imagePullPolicy: {{ $root.Values.image.pullPolicy }}
          command: [/sniffarr]
          args:
            {{- if .sniff }}
            - "-sniff"
            {{- end }}
            {{- if .fetch }}
            - "-fetch"
            {{- end }}
            {{- with include "sniffarr.dhtArgs" (dict "flags" $v.flags "nodePort" $nodePort "upnp" $v.upnp) | trim }}
            {{- . | nindent 12 }}
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
          {{- if and $port (or .sniff $v.hostNetwork $svc.enabled) }}
          ports:
            {{- include "sniffarr.dhtContainerPorts" (dict "port" $port) | trim | nindent 12 }}
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
{{ include "sniffarr.dhtService" (dict "root" $root "name" (printf "%s-%s" $root.Release.Name .component) "component" .component "service" $svc "port" $port "nodePort" $nodePort) }}
{{- end }}
{{- end }}
