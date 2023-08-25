package main

import (
	"fmt"

	"github.com/kelseyhightower/envconfig"
	// cloudrun "github.com/pulumi/pulumi-gcp-global-cloudrun/sdk/go/gcp"

	// "github.com/pulumi/pulumi-gcp/sdk/v6/go/gcp/artifactregistry"

	// "github.com/pulumi/pulumi-gcp/sdk/v6/go/gcp/cloudrunv2"

	cloudrun "github.com/pulumi/pulumi-google-native/sdk/go/google/run/v2"

	artifactregistry "github.com/pulumi/pulumi-google-native/sdk/go/google/artifactregistry/v1"

	cloudbuild "github.com/pulumi/pulumi-google-native/sdk/go/google/cloudbuild/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/rs/zerolog/log"
)

const GCP_DOCKER_REGISTRY = "us-docker.pkg.dev"

type EnvConfig struct {
	ServiceName string `envconfig:"SERVICE_NAME" required:"true"`
	Project     string `envconfig:"GCP_PROJECT" required:"true"`
	Region      string `envconfig:"GCP_REGION" default:"us-central1"`
	Debug       bool   `envconfig:"DEBUG"`
}

func main() {
	var envVars EnvConfig
	err := envconfig.Process("", &envVars)
	if err != nil {
		log.Fatal().Err(err)
	}

	projectID := envVars.Project

	pulumi.Run(func(ctx *pulumi.Context) error {

		serviceName := envVars.ServiceName
		image := fmt.Sprintf("%s/%s/%s/api", GCP_DOCKER_REGISTRY, projectID, serviceName)

		err := createDockerArtifactRepository(ctx, serviceName, projectID)
		if err != nil {
			return err
		}

		// trigger an initial build. subsequent builds would be handled by triggers
		// err = createCloudBuild(ctx, image, serviceName, projectID, envVars.Region)
		if err != nil {
			log.Warn().Err(err)
			return err
		}

		err = createCloudRunDeployment(ctx, image, serviceName, projectID, envVars.Region)
		if err != nil {
			return err
		}

		// TODO setup external load balancer

		return nil
	})
}

func createCloudRunDeployment(ctx *pulumi.Context, image string, serviceName string, projectID string, region string) error {
	// TODO add a trigger for ContinousDelivery

	_, err := cloudrun.NewService(ctx, serviceName, &cloudrun.ServiceArgs{
		ServiceId:   pulumi.String(serviceName),
		Project:     pulumi.String(projectID),
		Description: pulumi.String(fmt.Sprintf("cloud run instance of %s", serviceName)),
		Location:    pulumi.String(region),
		// TODO make me public
		Ingress: cloudrun.ServiceIngressIngressTrafficInternalOnly,
		Template: &cloudrun.GoogleCloudRunV2RevisionTemplateArgs{
			Containers: &cloudrun.GoogleCloudRunV2ContainerArray{
				&cloudrun.GoogleCloudRunV2ContainerArgs{
					Image: pulumi.String(image),
				},
			},
		},
		// TODO split traffic between revs with Traffic
	}, pulumi.Timeouts(&pulumi.CustomTimeouts{Create: "5m"}))
	if err != nil {
		return err
	}

	// ctx.Export("ip", deployment.IpAddress)
	return nil
}

func createCloudBuild(ctx *pulumi.Context, image string, serviceName string, projectID string, region string) error {
	var buildSteps cloudbuild.BuildStepArray
	buildSteps = append(buildSteps, &cloudbuild.BuildStepArgs{
		Name: pulumi.String("gcr.io/cloud-builders/docker"),
		Dir:  pulumi.String("."),
		Args: pulumi.StringArray{
			pulumi.String("build"),
			pulumi.String("-t"),
			pulumi.String(image),
			pulumi.String("."),
		},
		// TODO in a developer platform there would be an automated workflow
		// to run "pulumi up".
		// E.g.: an Argo Workflow that runs on every pull request.
		// said workflow would then pull the source code from the repo.
		// until then, Cloud Build will be unable to fetch the code
		AllowFailure: pulumi.Bool(true),
		AllowExitCodes: pulumi.IntArray{
			pulumi.Int(128),
		},
	})
	_, err := cloudbuild.NewBuild(ctx, serviceName, &cloudbuild.BuildArgs{
		ProjectId: pulumi.String(projectID),
		Project:   pulumi.String(projectID),
		Location:  pulumi.String(region),
		Steps:     buildSteps,
		Images: pulumi.StringArray{
			pulumi.String(image),
		},
		Source: &cloudbuild.SourceArgs{
			// TODO configure credentials + pulumi secrets
			GitSource: &cloudbuild.GitSourceArgs{
				Dir: pulumi.String("."),
				// TODO read from pulumi properties
				Url:      pulumi.String("https://github.com/davidmontoyago/pulumi-cloud-run-service.git"),
				Revision: pulumi.String("HEAD"),
			},
		},
	})
	return err
}

func createDockerArtifactRepository(ctx *pulumi.Context, serviceName string, projectID string) error {
	_, err := artifactregistry.NewRepository(ctx, serviceName, &artifactregistry.RepositoryArgs{
		Description:  pulumi.String(fmt.Sprintf("docker images for service %s", serviceName)),
		Format:       artifactregistry.RepositoryFormatPtr("DOCKER"),
		Location:     pulumi.String("us"),
		RepositoryId: pulumi.String(serviceName),
		Project:      pulumi.StringPtr(projectID),
	})
	return err
}
