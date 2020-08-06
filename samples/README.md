## Service Discovery Configurations
### Mount Config Map as Files
Since we are using file-based service discovery,
we need to mount the config maps as files. To do that,
add a volume mount to prometheu server
you want to mount the config map to.
In the sample, we mount to /etc/file_sd, but this could
be your choice. However, `file-sd-config` is a pre-compiled
config map name and should not be changed if you are going
to use the provided Docker image. You could use other names
if you change the part of the code where we name
the config map, re-build the binary and host your
own Docker image.
```yaml
volumeMounts:
  - name: file-sd-volume
    mountPath: /etc/file_sd
volumes:
  - name: file-sd-volume
    configMap:
      name: file-sd-config  # should not change
      optional: true
```

### Endpoint Filtering
For the endpoints of the VM to be discovered, we added
a new scrape job so that only the endpoints read from files
will be kept.
```yaml
- job_name: kubernetes-file-sd-endpoints
  kubernetes_sd_configs:
  - role: endpoints
  file_sd_configs:
  - files:
    - /etc/file_sd/*.json
  relabel_configs:
  - action: keep    # keep only the endpoint with __meta_filepath
    regex: (.+)
    source_labels:
    - __meta_filepath
  - replacement: /stats/prometheus
    target_label: __metrics_path__
```
## Deploy the Sidecar
Deploy the Docker image hosted on [Docker hub](https://hub.docker.com/repository/docker/jackyzz/vm-discovery) as a sidecar.
You could build the binary from source and host your own docker image. 
```yaml
containers:
  - name: vm-discovery
    image: "jackyzz/vm-discovery:latest"
    imagePullPolicy: "IfNotPresent"
```

### Add Cluster Role to Prometheus 
In order for the Prometheus to read workload entries and
update the config map, we need to add cluster roles to the
prometheus configuration. We need `get`, `watch` and `list` for
workload entries, and write access to config maps.
```yaml
- apiGroups:
  - "networking.istio.io"
  verbs:
    - get
    - watch
    - list
  resources:
    - workloadentries
- apiGroups:
  - ""
  verbs:
    - get
    - watch
    - list
    - create
    - update
    - patch
    - delete
  resources:
    - configmaps
```