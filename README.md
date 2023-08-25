# pulumi-cloud-run-service

Quickly deploy a serverless backend in GCP with Pulumi & Go.

```
make deps

gcloud auth application-default login

# optional step. setup GCP project & services
make bootstrap

# launch
make deploy SERVICE_NAME=yoshimi-api GCP_PROJECT=xperiments
```