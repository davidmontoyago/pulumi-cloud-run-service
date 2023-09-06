# pulumi-cloud-run-service

Deploy a serverless backend in GCP with Pulumi & Go. Allows creating a global external load balancer in front of the Cloud Run service, in order to reuse the GCLB for other backends like a bucket, or to get Cloud Armor protection.

See `./main.go` for the infra stuff.

See:
- https://github.com/ahmetb/cloud-run-faq/blob/master/README.md#how-does-cloud-runs-load-balancing-compare-with-cloud-load-balancer-gclb
- https://cloud.google.com/load-balancing/docs/https/setting-up-https-serverless

## auth to GCP
```
gcloud auth application-default login
```

##  if not already, configure GCP project & services
```
make bootstrap
```

## configure pulumi stack
```
pulumi stack init <my-stack>
```

## configure
```
export GCP_RUN_SERVICE_NAME="my-bot-api"
export GCP_PROJECT="my-bots-project"
export GCP_REGION="us-central-1"
export GCP_NETWORK="default"
export GCP_RUN_SERVICE_UNAUTHENTICATED_ENABLE=true
export GCP_EXTERNAL_LOAD_BALANCER_ENABLE=true
export GCP_EXTERNAL_LOAD_BALANCER_TLS_ENABLE=true
export GCP_EXTERNAL_LOAD_BALANCER_HTTP_FORWARD_ENABLE=true
export GCP_EXTERNAL_LOAD_BALANCER_HTTPS_REDIRECT_ENABLE=true
export GCP_EXTERNAL_LOAD_BALANCER_CLOUD_ARMOR_ENABLE=true
export GCP_EXTERNAL_LOAD_BALANCER_TLS_DOMAIN="my-bot-api.example.org"
export GCP_CLOUD_BUILD_SOURCE_REPO_URL=<repo url for cloud build>
export GCP_ARTIFACT_REGISTRY_URL=us-docker.pkg.dev
```

## launch
```
pulumi up
```