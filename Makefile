PROJECT_NAME := "bots-backend-1"
SERVICE_NAME := "yoshimi-api"
GCP_PROJECT := ${GCP_PROJECT}

deps:
	brew install buildpacks/tap/pack

bootstrap:
	# gcloud projects create $(PROJECT_NAME)
	gcloud config set project $(PROJECT_NAME)
	gcloud services enable compute.googleapis.com --project $(PROJECT_NAME)
	gcloud services enable artifactregistry.googleapis.com --project $(PROJECT_NAME)
	gcloud services enable cloudbuild.googleapis.com  --project $(PROJECT_NAME)
	gcloud services enable run.googleapis.com --project $(PROJECT_NAME)

image:
	docker build -t us-docker.pkg.dev/bots-backend-1/hirebot-api/api .

build:
	cd ./app && go build
	go build ./...

deploy: build
	SERVICE_NAME=$(SERVICE_NAME) GCP_PROJECT=$(GCP_PROJECT) pulumi up --verbose=2