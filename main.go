package main

import (
	"fmt"

	"github.com/kelseyhightower/envconfig"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/rs/zerolog/log"
)

type EnvConfig struct {
	ServiceName                string `envconfig:"GCP_RUN_SERVICE_NAME" required:"true"`
	Project                    string `envconfig:"GCP_PROJECT" required:"true"`
	Region                     string `envconfig:"GCP_REGION" default:"us-central1"`
	Network                    string `envconfig:"GCP_NETWORK" default:"default"`
	EnableUnauthenticated      bool   `envconfig:"GCP_RUN_SERVICE_UNAUTHENTICATED_ENABLE" default:"false"`
	EnableExternalLoadBalancer bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_ENABLE" default:"false"`
	EnableTLS                  bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_TLS_ENABLE" default:"false"`
	EnableHTTPForward          bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_HTTP_FORWARD_ENABLE" default:"false"`
	EnableHTTPSRedirect        bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_HTTPS_REDIRECT_ENABLE" default:"false"`
	EnableCloudArmor           bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_CLOUD_ARMOR_ENABLE" default:"false"`
	TLSDomainName              string `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_TLS_DOMAIN" required:"false"`
	ProxyOnlySubnetIPRange     string `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_PROXY_ONLY_SUBNET_CIDR" default:"10.127.0.0/24"`
	CloudBuildSourceRepoURL    string `envconfig:"GCP_CLOUD_BUILD_SOURCE_REPO_URL" default:"https://github.com/davidmontoyago/pulumi-cloud-run-service.git"`
	CloudBuildBuilderImage     string `envconfig:"GCP_CLOUD_BUILD_DOCKER_BUILDER_IMAGE" default:"gcr.io/cloud-builders/docker"`
	ArtifactRegistryURL        string `envconfig:"GCP_ARTIFACT_REGISTRY_URL" default:"us-docker.pkg.dev"`
	Debug                      bool   `envconfig:"DEBUG"`
}

func main() {
	var envVars EnvConfig
	envconfig.MustProcess("", &envVars)

	pulumi.Run(func(ctx *pulumi.Context) error {

		stack := &serverlessStack{
			config: envVars,
		}

		err := stack.createDockerArtifactRepository(ctx)
		if err != nil {
			return err
		}

		image := fmt.Sprintf("%s/%s/%s/api", stack.config.ArtifactRegistryURL, stack.config.Project, stack.config.ServiceName)

		// trigger an initial build. subsequent builds would be handled by triggers
		// err = stack.createCloudBuild(ctx, image)
		if err != nil {
			log.Warn().Err(err)
			return err
		}

		err = stack.createCloudRunDeployment(ctx, image)
		if err != nil {
			return err
		}

		if envVars.EnableExternalLoadBalancer {
			// add an optional GCLB to support Cloud CDN, Armor and IAP
			// See:
			// https://github.com/ahmetb/cloud-run-faq/blob/master/README.md#how-does-cloud-runs-load-balancing-compare-with-cloud-load-balancer-gclb
			err = stack.createExternalLoadBalancer(ctx)
			if err != nil {
				return err
			}
		}

		return nil
	})
}
