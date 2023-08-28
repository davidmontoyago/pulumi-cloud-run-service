package main

import (
	"fmt"

	"github.com/kelseyhightower/envconfig"

	// cloudrun "github.com/pulumi/pulumi-gcp-global-cloudrun/sdk/go/gcp"
	// "github.com/pulumi/pulumi-gcp/sdk/v6/go/gcp/artifactregistry"
	// "github.com/pulumi/pulumi-gcp/sdk/v6/go/gcp/cloudrunv2"

	computeclassic "github.com/pulumi/pulumi-gcp/sdk/v5/go/gcp/compute"
	artifactregistry "github.com/pulumi/pulumi-google-native/sdk/go/google/artifactregistry/v1"
	cloudbuild "github.com/pulumi/pulumi-google-native/sdk/go/google/cloudbuild/v1"
	compute "github.com/pulumi/pulumi-google-native/sdk/go/google/compute/v1"
	cloudrunv1 "github.com/pulumi/pulumi-google-native/sdk/go/google/run/v1"
	cloudrun "github.com/pulumi/pulumi-google-native/sdk/go/google/run/v2"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/rs/zerolog/log"
)

const GCP_DOCKER_REGISTRY = "us-docker.pkg.dev"

type EnvConfig struct {
	ServiceName                string `envconfig:"SERVICE_NAME" required:"true"`
	Project                    string `envconfig:"GCP_PROJECT" required:"true"`
	Region                     string `envconfig:"GCP_REGION" default:"us-central1"`
	Network                    string `envconfig:"GCP_NETWORK" required:"true"`
	Debug                      bool   `envconfig:"DEBUG"`
	EnableExternalLoadBalancer bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_ENABLE"`
	EnableHTTPForward          bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_HTTP_FORWARD_ENABLE" default:"true"`
	EnableTLS                  bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_TLS_ENABLE" default:"false"`
}

func main() {
	var envVars EnvConfig
	envconfig.MustProcess("", &envVars)

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

		if envVars.EnableExternalLoadBalancer {
			// add an optional GCLB to support Cloud CDN, Armor and IAP
			// See:
			// https://github.com/ahmetb/cloud-run-faq/blob/master/README.md#how-does-cloud-runs-load-balancing-compare-with-cloud-load-balancer-gclb
			network := fmt.Sprintf("projects/%s/global/networks/%s", projectID, envVars.Network)
			err = createExternalLoadBalancer(ctx, serviceName, network, projectID,
				envVars.Region, envVars.EnableHTTPForward, envVars.EnableTLS)
			if err != nil {
				return err
			}
		}

		return nil
	})
}

// createExternalLoadBalancer setups a regional classic Application Load Balancer
// with the following feats:
//
// - HTTPS by default with GCP managed certificate, HTTP if enabled
// - IAP if enabled
// - IP blocklisting if enabled
//
// See:
// https://cloud.google.com/load-balancing/docs/https/setting-up-reg-ext-https-serverless
func createExternalLoadBalancer(ctx *pulumi.Context, serviceName, network, projectID,
	region string, httpForward, tls bool) error {

	// proxy-only subnet required by Cloud Run to get traffic from the LB
	_, err := compute.NewSubnetwork(ctx, fmt.Sprintf("%s-proxy-only", serviceName), &compute.SubnetworkArgs{
		Description: pulumi.String(fmt.Sprintf("proxy-only subnet for cloud run traffic for %s", serviceName)),
		Project:     pulumi.String(projectID),
		Region:      pulumi.String(region),
		Purpose:     compute.SubnetworkPurposeRegionalManagedProxy,
		Network:     pulumi.String(network),
		// Extended subnetworks in auto subnet mode networks cannot overlap with 10.128.0.0/9
		IpCidrRange: pulumi.String("10.127.0.0/24"),
		Role:        compute.SubnetworkRoleActive,
	})
	if err != nil {
		return err
	}

	neg, err := compute.NewRegionNetworkEndpointGroup(ctx, fmt.Sprintf("%s-default", serviceName), &compute.RegionNetworkEndpointGroupArgs{
		Description:         pulumi.String(fmt.Sprintf("NEG to LB traffic for %s", serviceName)),
		Project:             pulumi.String(projectID),
		Region:              pulumi.String(region),
		NetworkEndpointType: compute.RegionNetworkEndpointGroupNetworkEndpointTypeServerless,
		CloudRun: &compute.NetworkEndpointGroupCloudRunArgs{
			Service: pulumi.String(serviceName),
		},
	})
	if err != nil {
		return err
	}

	service, err := compute.NewRegionBackendService(ctx, fmt.Sprintf("%s-default", serviceName), &compute.RegionBackendServiceArgs{
		Description: pulumi.String(fmt.Sprintf("service backend for %s", serviceName)),
		Project:     pulumi.String(projectID),
		// TODO change to https
		Protocol:            compute.RegionBackendServiceProtocolHttp,
		LoadBalancingScheme: compute.RegionBackendServiceLoadBalancingSchemeExternalManaged,
		Region:              pulumi.String(region),
		// TODO setup heathlcheck
		Backends: compute.BackendArray{
			&compute.BackendArgs{
				Group: neg.SelfLink,
			},
		},
		// TODO allow enabling IAP (Identity Aware Proxy)
	})
	if err != nil {
		return err
	}

	// TODO create compute address if enabled
	urlMap, err := compute.NewRegionUrlMap(ctx, fmt.Sprintf("%s-default", serviceName), &compute.RegionUrlMapArgs{
		Description: pulumi.String(fmt.Sprintf("URL map to LB traffic for %s", serviceName)),
		Project:     pulumi.String(projectID),
		// TODO configure
		Region:         pulumi.String(region),
		DefaultService: service.SelfLink,
	})
	if err != nil {
		return err
	}

	// TODO setup UrlMAP for HTTPS redirect
	// https://github.com/terraform-google-modules/terraform-google-lb-http/blob/2a11956a2ed58fd60f1dde5a8277b8aeef70e6db/main.tf#L171

	if tls {
		certificate, err := computeclassic.NewManagedSslCertificate(ctx, fmt.Sprintf("%s-tls", serviceName), &computeclassic.ManagedSslCertificateArgs{
			Description: pulumi.String(fmt.Sprintf("TLS cert for %s", serviceName)),
			Project:     pulumi.String(projectID),
			Managed: &computeclassic.ManagedSslCertificateManagedArgs{
				Domains: pulumi.StringArray{
					// TODO allow configurable
					pulumi.String("pathtoprod.dev"),
				},
			},
		})
		if err != nil {
			return err
		}

		httpsProxy, err := compute.NewRegionTargetHttpsProxy(ctx, fmt.Sprintf("%s-https", serviceName), &compute.RegionTargetHttpsProxyArgs{
			Description: pulumi.String(fmt.Sprintf("proxy to LB traffic for %s", serviceName)),
			Project:     pulumi.String(projectID),
			Region:      pulumi.String(region),
			UrlMap:      urlMap.SelfLink,
			SslCertificates: pulumi.StringArray{
				certificate.SelfLink,
			},
		})
		if err != nil {
			return err
		}

		_, err = compute.NewForwardingRule(ctx, fmt.Sprintf("%s-https", serviceName), &compute.ForwardingRuleArgs{
			Description: pulumi.String(fmt.Sprintf("HTTPS forwarding rule to LB traffic for %s", serviceName)),
			Project:     pulumi.String(projectID),
			Network:     pulumi.String(network),
			Region:      pulumi.String(region),
			PortRange:   pulumi.String("443"),
			NetworkTier: compute.ForwardingRuleNetworkTierStandard,
			// TODO make configurable
			LoadBalancingScheme: compute.ForwardingRuleLoadBalancingSchemeExternalManaged,
			Target:              httpsProxy.SelfLink,
		})
		if err != nil {
			return err
		}
	}

	if httpForward {
		httpProxy, err := compute.NewRegionTargetHttpProxy(ctx, fmt.Sprintf("%s-http", serviceName), &compute.RegionTargetHttpProxyArgs{
			Description: pulumi.String(fmt.Sprintf("proxy to LB traffic for %s", serviceName)),
			Project:     pulumi.String(projectID),
			UrlMap:      urlMap.SelfLink,
			Region:      pulumi.String(region),
		})
		if err != nil {
			return err
		}

		_, err = compute.NewForwardingRule(ctx, fmt.Sprintf("%s-http", serviceName), &compute.ForwardingRuleArgs{
			Description: pulumi.String(fmt.Sprintf("HTTP forwarding rule to LB traffic for %s", serviceName)),
			Project:     pulumi.String(projectID),
			Network:     pulumi.String(network),
			Region:      pulumi.String(region),
			PortRange:   pulumi.String("80"),
			NetworkTier: compute.ForwardingRuleNetworkTierStandard,
			// TODO make configurable
			LoadBalancingScheme: compute.ForwardingRuleLoadBalancingSchemeExternalManaged,
			Target:              httpProxy.SelfLink,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func createCloudRunDeployment(ctx *pulumi.Context, image string, serviceName string, projectID string, region string) error {
	// TODO add a trigger for ContinousDelivery

	_, err := cloudrun.NewService(ctx, serviceName, &cloudrun.ServiceArgs{
		ServiceId:   pulumi.String(serviceName),
		Project:     pulumi.String(projectID),
		Description: pulumi.String(fmt.Sprintf("cloud run instance of %s", serviceName)),
		Location:    pulumi.String(region),
		Ingress:     cloudrun.ServiceIngressIngressTrafficInternalLoadBalancer,
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

	// TODO enable only if external LB enabled
	if 1 == 0 {
		_, err = cloudrunv1.NewServiceIamPolicy(ctx, serviceName, &cloudrunv1.ServiceIamPolicyArgs{
			Location:  pulumi.String(region),
			Project:   pulumi.String(projectID),
			ServiceId: pulumi.String(serviceName),
			Bindings: &cloudrunv1.BindingArray{
				cloudrunv1.BindingArgs{
					Role: pulumi.String("roles/run.invoker"),
					Members: pulumi.StringArray{
						pulumi.String("allUsers"),
					},
				},
			},
		})
		if err != nil {
			return err
		}
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
