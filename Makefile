GCP_PROJECT := "bots-backend-1"
SERVICE_NAME := "yoshimi-api"
GCP_NETWORK := "bots-backend-1"

.PHONY: build

deps:
	brew install buildpacks/tap/pack

bootstrap:
	# gcloud projects create $(GCP_PROJECT)
	gcloud config set project $(GCP_PROJECT)
	gcloud services enable compute.googleapis.com --project $(GCP_PROJECT)
	gcloud services enable artifactregistry.googleapis.com --project $(GCP_PROJECT)
	gcloud services enable cloudbuild.googleapis.com  --project $(GCP_PROJECT)
	gcloud services enable run.googleapis.com --project $(GCP_PROJECT)
	# for optional features

image:
	docker build -t us-docker.pkg.dev/bots-backend-1/hirebot-api/api .

build:
	cd ./app && go build -o ./build/
	go build -o ./build/

deploy: build
	SERVICE_NAME=$(SERVICE_NAME) \
	GCP_PROJECT=$(GCP_PROJECT) \
	GCP_NETWORK=$(GCP_NETWORK) \
	GCP_EXTERNAL_LOAD_BALANCER_ENABLE=$(GCP_EXTERNAL_LOAD_BALANCER_ENABLE) \
	pulumi up --verbose=2