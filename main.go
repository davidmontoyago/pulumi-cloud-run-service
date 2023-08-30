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
	ServiceName                string `envconfig:"GCP_RUN_SERVICE_NAME" required:"true"`
	Project                    string `envconfig:"GCP_PROJECT" required:"true"`
	Region                     string `envconfig:"GCP_REGION" default:"us-central1"`
	Network                    string `envconfig:"GCP_NETWORK" default:"default"`
	Debug                      bool   `envconfig:"DEBUG"`
	EnableExternalLoadBalancer bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_ENABLE"`
	EnableHTTPForward          bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_HTTP_FORWARD_ENABLE" default:"false"`
	EnableTLS                  bool   `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_TLS_ENABLE" default:"false"`
	TLSDomainName              string `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_TLS_DOMAIN" required:"true"`
	ProxyOnlySubnetIPRange     string `envconfig:"GCP_EXTERNAL_LOAD_BALANCER_PROXY_ONLY_SUBNET_CIDR" default:"10.127.0.0/24"`
	EnableUnauthenticated      bool   `envconfig:"GCP_RUN_SERVICE_UNAUTHENTICATED_ENABLE" default:"false"`
}

func main() {
	var envVars EnvConfig
	envconfig.MustProcess("", &envVars)

	pulumi.Run(func(ctx *pulumi.Context) error {

		stack := &serverlessStack{
			config: envVars,
		}

		image := fmt.Sprintf("%s/%s/%s/api", GCP_DOCKER_REGISTRY, envVars.Project, envVars.ServiceName)

		err := stack.createDockerArtifactRepository(ctx, envVars.ServiceName)
		if err != nil {
			return err
		}

		// trigger an initial build. subsequent builds would be handled by triggers
		// err = stack.createCloudBuild(ctx, image, serviceName, projectID, envVars.Region)
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

type serverlessStack struct {
	config EnvConfig
}

// createExternalLoadBalancer sets up a global classic Application Load Balancer
// in front of the Run Service with the following feats:
//
// - HTTPS by default with GCP managed certificate
// - HTTP forward & redirect to HTTPs
// - IAP if enabled
// - IP blocklisting if enabled
//
// See:
// https://cloud.google.com/load-balancing/docs/https/setting-up-https-serverless
func (s *serverlessStack) createExternalLoadBalancer(ctx *pulumi.Context) error {

	serviceName := s.config.ServiceName
	region := s.config.Region
	project := s.config.Project
	network := fmt.Sprintf("projects/%s/global/networks/%s", project, s.config.Network)

	// proxy-only subnet required by Cloud Run to get traffic from the LB
	// See:
	// https://cloud.google.com/load-balancing/docs/https#proxy-only-subnet
	_, err := compute.NewSubnetwork(ctx, fmt.Sprintf("%s-proxy-only", serviceName), &compute.SubnetworkArgs{
		Description: pulumi.String(fmt.Sprintf("proxy-only subnet for cloud run traffic for %s", serviceName)),
		Project:     pulumi.String(project),
		Region:      pulumi.String(region),
		Purpose:     compute.SubnetworkPurposeRegionalManagedProxy,
		Network:     pulumi.String(network),
		// Extended subnetworks in auto subnet mode networks cannot overlap with 10.128.0.0/9
		IpCidrRange: pulumi.String(s.config.ProxyOnlySubnetIPRange),
		Role:        compute.SubnetworkRoleActive,
	})
	if err != nil {
		return err
	}

	neg, err := compute.NewRegionNetworkEndpointGroup(ctx, fmt.Sprintf("%s-default", serviceName), &compute.RegionNetworkEndpointGroupArgs{
		Description:         pulumi.String(fmt.Sprintf("NEG to LB traffic for %s", serviceName)),
		Project:             pulumi.String(project),
		Region:              pulumi.String(region),
		NetworkEndpointType: compute.RegionNetworkEndpointGroupNetworkEndpointTypeServerless,
		CloudRun: &compute.NetworkEndpointGroupCloudRunArgs{
			Service: pulumi.String(serviceName),
		},
	})
	if err != nil {
		return err
	}

	service, err := compute.NewBackendService(ctx, fmt.Sprintf("%s-default", serviceName), &compute.BackendServiceArgs{
		Description:         pulumi.String(fmt.Sprintf("service backend for %s", serviceName)),
		Project:             pulumi.String(project),
		LoadBalancingScheme: compute.BackendServiceLoadBalancingSchemeExternal,
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
	urlMap, err := compute.NewUrlMap(ctx, fmt.Sprintf("%s-default", serviceName), &compute.UrlMapArgs{
		Description: pulumi.String(fmt.Sprintf("URL map to LB traffic for %s", serviceName)),
		Project:     pulumi.String(project),
		// TODO configure
		DefaultService: service.SelfLink,
	})
	if err != nil {
		return err
	}

	// TODO setup UrlMAP for HTTPS redirect
	// https://github.com/terraform-google-modules/terraform-google-lb-http/blob/2a11956a2ed58fd60f1dde5a8277b8aeef70e6db/main.tf#L171

	if s.config.EnableTLS {
		certificate, err := computeclassic.NewManagedSslCertificate(ctx, fmt.Sprintf("%s-tls", serviceName), &computeclassic.ManagedSslCertificateArgs{
			Description: pulumi.String(fmt.Sprintf("TLS cert for %s", serviceName)),
			Project:     pulumi.String(project),
			Managed: &computeclassic.ManagedSslCertificateManagedArgs{
				Domains: pulumi.StringArray{
					pulumi.String(s.config.TLSDomainName),
				},
			},
		})
		if err != nil {
			return err
		}

		httpsProxy, err := compute.NewTargetHttpsProxy(ctx, fmt.Sprintf("%s-https", serviceName), &compute.TargetHttpsProxyArgs{
			Description: pulumi.String(fmt.Sprintf("proxy to LB traffic for %s", serviceName)),
			Project:     pulumi.String(project),
			UrlMap:      urlMap.SelfLink,
			SslCertificates: pulumi.StringArray{
				certificate.SelfLink,
			},
		})
		if err != nil {
			return err
		}

		_, err = compute.NewGlobalForwardingRule(ctx, fmt.Sprintf("%s-https", serviceName), &compute.GlobalForwardingRuleArgs{
			Description: pulumi.String(fmt.Sprintf("HTTPS forwarding rule to LB traffic for %s", serviceName)),
			Project:     pulumi.String(project),
			PortRange:   pulumi.String("443"),
			NetworkTier: compute.GlobalForwardingRuleNetworkTierPremium,
			// TODO make configurable
			LoadBalancingScheme: compute.GlobalForwardingRuleLoadBalancingSchemeExternal,
			Target:              httpsProxy.SelfLink,
		})
		if err != nil {
			return err
		}
	}

	if s.config.EnableHTTPForward {
		httpProxy, err := compute.NewTargetHttpProxy(ctx, fmt.Sprintf("%s-http", serviceName), &compute.TargetHttpProxyArgs{
			Description: pulumi.String(fmt.Sprintf("proxy to LB traffic for %s", serviceName)),
			Project:     pulumi.String(project),
			UrlMap:      urlMap.SelfLink,
		})
		if err != nil {
			return err
		}

		_, err = compute.NewGlobalForwardingRule(ctx, fmt.Sprintf("%s-http", serviceName), &compute.GlobalForwardingRuleArgs{
			Description: pulumi.String(fmt.Sprintf("HTTP forwarding rule to LB traffic for %s", serviceName)),
			Project:     pulumi.String(project),
			PortRange:   pulumi.String("80"),
			NetworkTier: compute.GlobalForwardingRuleNetworkTierPremium,
			// TODO make configurable
			LoadBalancingScheme: compute.GlobalForwardingRuleLoadBalancingSchemeExternal,
			Target:              httpProxy.SelfLink,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *serverlessStack) createCloudRunDeployment(ctx *pulumi.Context, image string) error {
	// TODO add a trigger for ContinousDelivery

	project := s.config.Project
	serviceName := s.config.ServiceName
	region := s.config.Region

	_, err := cloudrun.NewService(ctx, serviceName, &cloudrun.ServiceArgs{
		ServiceId:   pulumi.String(serviceName),
		Project:     pulumi.String(project),
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

	if s.config.EnableUnauthenticated {
		_, err = cloudrunv1.NewServiceIamPolicy(ctx, serviceName, &cloudrunv1.ServiceIamPolicyArgs{
			Location:  pulumi.String(region),
			Project:   pulumi.String(project),
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

func (s *serverlessStack) createCloudBuild(ctx *pulumi.Context, image string, serviceName string, region string) error {
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
		ProjectId: pulumi.String(s.config.Project),
		Project:   pulumi.String(s.config.Project),
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

func (s *serverlessStack) createDockerArtifactRepository(ctx *pulumi.Context, serviceName string) error {
	_, err := artifactregistry.NewRepository(ctx, serviceName, &artifactregistry.RepositoryArgs{
		Description:  pulumi.String(fmt.Sprintf("docker images for service %s", serviceName)),
		Format:       artifactregistry.RepositoryFormatPtr("DOCKER"),
		Location:     pulumi.String("us"),
		RepositoryId: pulumi.String(serviceName),
		Project:      pulumi.StringPtr(s.config.Project),
	})
	return err
}
