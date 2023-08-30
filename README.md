# pulumi-cloud-run-service

Deploy a serverless backend in GCP with Pulumi & Go. Allows creating a global external load balancer in front of the Cloud Run service, in order to reuse the GCLB for other backends like a bucket, or to get Cloud Armor protection.

See:
- https://github.com/ahmetb/cloud-run-faq/blob/master/README.md#how-does-cloud-runs-load-balancing-compare-with-cloud-load-balancer-gclb
- https://cloud.google.com/load-balancing/docs/https/setting-up-https-serverless

## install prereqs
```
make deps
```

## auth to GCP
```
gcloud auth application-default login
```

##  if not already, configure GCP project & services
```
make bootstrap
```

## configure
TODO vars
```
```

## launch
``````
make deploy
```