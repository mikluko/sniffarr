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
fsGroup: 65532
fsGroupChangePolicy: OnRootMismatch
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
sniffarr.waitAPI is the init container wait-api, sniffarr -wait-api against url, when
waitForAPI.enabled, else nothing. It takes root and url.
*/}}
{{- define "sniffarr.waitAPI" -}}
{{- $w := .root.Values.waitForAPI }}
{{- if $w.enabled }}
- name: wait-api
  image: {{ include "sniffarr.image" .root }}
  imagePullPolicy: {{ .root.Values.image.pullPolicy }}
  command: [/sniffarr]
  args:
    - "-wait-api"
  env:
    - name: SNIFFARR_URL
      value: {{ .url | quote }}
  {{- with $w.resources }}
  resources:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  securityContext:
    {{- include "sniffarr.securityContext" .root | nindent 4 }}
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
(the pods it selects), service, port and nodePort, and pod, the one pod it selects when set.
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
    {{- with .pod }}
    statefulset.kubernetes.io/pod-name: {{ . }}
    {{- end }}
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
the litestream ConfigMap when enabled. It takes root, claim, the name of the claim the chart
creates, and template, true when a StatefulSet's claim template is data and none is rendered here.
*/}}
{{- define "sniffarr.dataVolumes" -}}
{{- $p := .root.Values.api.persistence }}
{{- $ls := .root.Values.api.litestream }}
{{- if not (has $p.type (list "emptyDir" "hostPath" "pvc")) }}
{{- fail (printf "api.persistence.type: want emptyDir, hostPath or pvc, not %q" $p.type) }}
{{- end }}
- name: tmp
  emptyDir: {}
{{- if not .template }}
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
{{- end }}
{{- if $ls.enabled }}
- name: litestream-config
  configMap:
    name: {{ required "api.litestream.existingConfigMap is required when litestream is enabled" $ls.existingConfigMap }}
{{- end }}
{{- end }}

{{- /* sniffarr.probeArgs renders -probe and the flags of probe, given as probe. */}}
{{- define "sniffarr.probeArgs" -}}
- "-probe"
- "-probe-dir=/seeds"
- {{ printf "-probers=%v" .probers | quote }}
- {{ printf "-probe-timeout=%v" .timeout | quote }}
- {{ printf "-seed-max-size=%v" .maxSize | quote }}
- {{ printf "-seed-max-age=%v" .maxAge | quote }}
{{- end }}

{{- /* sniffarr.seedsType is probe.persistence.type, checked. */}}
{{- define "sniffarr.seedsType" -}}
{{- if not (has .type (list "emptyDir" "hostPath" "pvc")) }}
{{- fail (printf "probe.persistence.type: want emptyDir, hostPath or pvc, not %q" .type) }}
{{- end }}
{{- .type }}
{{- end }}

{{- /*
sniffarr.seedsVolume is the volume seeds of /seeds per probe.persistence, given as probe, with
claim the name of the claim mounted when the type is pvc.
*/}}
{{- define "sniffarr.seedsVolume" -}}
{{- $p := .probe.persistence }}
{{- $type := include "sniffarr.seedsType" $p }}
- name: seeds
  {{- if eq $type "hostPath" }}
  hostPath:
    path: {{ required "probe.persistence.hostPath.path is required for type hostPath" $p.hostPath.path }}
    type: DirectoryOrCreate
  {{- else if eq $type "pvc" }}
  persistentVolumeClaim:
    claimName: {{ .claim }}
  {{- else }}
  emptyDir: {}
  {{- end }}
{{- end }}

{{- /* sniffarr.seedsClaimSpec is the spec of a claim of /seeds per probe.persistence. */}}
{{- define "sniffarr.seedsClaimSpec" -}}
accessModes: [ReadWriteOnce]
{{- with .storageClass }}
storageClassName: {{ . }}
{{- end }}
resources:
  requests:
    storage: {{ .size }}
{{- end }}

{{- /* sniffarr.seedsClaim is the claim of /seeds of the single process, when it probes into one. */}}
{{- define "sniffarr.seedsClaim" -}}
{{- $p := .Values.probe.persistence }}
{{- if and .Values.single.enabled .Values.fetch.enabled .Values.probe.enabled (eq (include "sniffarr.seedsType" $p) "pvc") }}
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{ .Release.Name }}-seeds
  labels:
    {{- include "sniffarr.labels" . | nindent 4 }}
    app.kubernetes.io/component: probe
spec:
  {{- include "sniffarr.seedsClaimSpec" $p | nindent 2 }}
{{- end }}
{{- end }}

{{- /*
sniffarr.dht is a Deployment of sniffarr with its DHT components, and the Service of its DHT
port when v.service.enabled. It takes root, component, v (the component's values), sniff and
fetch (the flags), url, and probe, the probe values for the probe component, which make it a
StatefulSet with a /seeds per replica, its headless Service of the component's name, each pod
on the port plus its index, and with v.service.enabled a Service per pod, <name>-<index>.
*/}}
{{- define "sniffarr.dht" -}}
{{- $root := .root }}
{{- $v := .v }}
{{- $svc := $v.service }}
{{- $dht := include "sniffarr.dhtPort" (dict "component" .component "flags" $v.flags "service" $svc) | fromYaml }}
{{- $port := int $dht.port }}
{{- $nodePort := int $dht.nodePort }}
{{- $probe := .probe }}
{{- $seeds := "" }}
{{- if $probe }}
{{- $seeds = include "sniffarr.seedsType" $probe.persistence }}
{{- end }}
apiVersion: apps/v1
kind: {{ if $probe }}StatefulSet{{ else }}Deployment{{ end }}
metadata:
  name: {{ $root.Release.Name }}-{{ .component }}
  labels:
    {{- include "sniffarr.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
spec:
  replicas: {{ $v.replicas }}
  {{- if $probe }}
  serviceName: {{ $root.Release.Name }}-{{ .component }}
  podManagementPolicy: Parallel
  {{- else if $v.hostNetwork }}
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
      {{- $url := .url | default (printf "http://%s-api:8080" $root.Release.Name) }}
      {{- $wait := include "sniffarr.waitAPI" (dict "root" $root "url" $url) | trim }}
      {{- $extra := list }}
      {{- if $probe }}
      {{- $extra = $probe.extraInitContainers }}
      {{- end }}
      {{- if or $wait $extra }}
      initContainers:
        {{- with $wait }}
        {{- . | nindent 8 }}
        {{- end }}
        {{- with $extra }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
      {{- end }}
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
            {{- if $probe }}
            {{- include "sniffarr.probeArgs" $probe | trim | nindent 12 }}
            {{- if $port }}
            - "-port-offset=$(POD_INDEX)"
            {{- end }}
            {{- end }}
          env:
            - name: SNIFFARR_URL
              value: {{ $url | quote }}
            {{- if $probe }}
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: POD_INDEX
              valueFrom:
                fieldRef:
                  fieldPath: metadata.labels['apps.kubernetes.io/pod-index']
            {{- end }}
            {{- with include "sniffarr.env" (dict "root" $root "env" $v.env) | trim }}
            {{- . | nindent 12 }}
            {{- end }}
          {{- if and $port (not $probe) (or .sniff $v.hostNetwork $svc.enabled) }}
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
            {{- if $probe }}
            - name: seeds
              mountPath: /seeds
              {{- if eq $seeds "hostPath" }}
              subPathExpr: $(POD_NAME)
              {{- end }}
            {{- end }}
      volumes:
        - name: tmp
          emptyDir: {}
        {{- if and $probe (ne $seeds "pvc") }}
        {{- include "sniffarr.seedsVolume" (dict "probe" $probe) | trim | nindent 8 }}
        {{- end }}
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
  {{- if eq $seeds "pvc" }}
  volumeClaimTemplates:
    - metadata:
        name: seeds
      spec:
        {{- include "sniffarr.seedsClaimSpec" $probe.persistence | nindent 8 }}
  {{- end }}
{{- if $probe }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $root.Release.Name }}-{{ .component }}
  labels:
    {{- include "sniffarr.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
spec:
  clusterIP: None
  selector:
    {{- include "sniffarr.selector" (dict "root" $root "component" .component) | nindent 4 }}
{{- end }}
{{- if and $svc.enabled $probe }}
{{- range $i := until (int $v.replicas) }}
{{- $pod := printf "%s-%s-%d" $root.Release.Name $.component $i }}
---
{{ include "sniffarr.dhtService" (dict "root" $root "name" $pod "pod" $pod "component" $.component "service" $svc "port" (add $port $i) "nodePort" (ternary (add $nodePort $i) 0 (gt $nodePort 0))) }}
{{- end }}
{{- else if $svc.enabled }}
---
{{ include "sniffarr.dhtService" (dict "root" $root "name" (printf "%s-%s" $root.Release.Name .component) "component" .component "service" $svc "port" $port "nodePort" $nodePort) }}
{{- end }}
{{- end }}
