package main

import (
	"fmt"

	computeclassic "github.com/pulumi/pulumi-gcp/sdk/v5/go/gcp/compute"
	artifactregistry "github.com/pulumi/pulumi-google-native/sdk/go/google/artifactregistry/v1"
	cloudbuild "github.com/pulumi/pulumi-google-native/sdk/go/google/cloudbuild/v1"
	compute "github.com/pulumi/pulumi-google-native/sdk/go/google/compute/v1"
	cloudrunv1 "github.com/pulumi/pulumi-google-native/sdk/go/google/run/v1"
	cloudrun "github.com/pulumi/pulumi-google-native/sdk/go/google/run/v2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type serverlessStack struct {
	config EnvConfig
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

func (s *serverlessStack) createCloudBuild(ctx *pulumi.Context, image string) error {
	var buildSteps cloudbuild.BuildStepArray
	buildSteps = append(buildSteps, &cloudbuild.BuildStepArgs{
		Name: pulumi.String(s.config.CloudBuildBuilderImage),
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
	_, err := cloudbuild.NewBuild(ctx, s.config.ServiceName, &cloudbuild.BuildArgs{
		ProjectId: pulumi.String(s.config.Project),
		Project:   pulumi.String(s.config.Project),
		Location:  pulumi.String(s.config.Region),
		Steps:     buildSteps,
		Images: pulumi.StringArray{
			pulumi.String(image),
		},
		Source: &cloudbuild.SourceArgs{
			// TODO configure credentials + pulumi secrets
			GitSource: &cloudbuild.GitSourceArgs{
				Dir:      pulumi.String("."),
				Url:      pulumi.String(s.config.CloudBuildSourceRepoURL),
				Revision: pulumi.String("HEAD"),
			},
		},
	})
	return err
}

func (s *serverlessStack) createDockerArtifactRepository(ctx *pulumi.Context) error {
	serviceName := s.config.ServiceName
	_, err := artifactregistry.NewRepository(ctx, serviceName, &artifactregistry.RepositoryArgs{
		Description:  pulumi.String(fmt.Sprintf("docker images for service %s", serviceName)),
		Format:       artifactregistry.RepositoryFormatPtr("DOCKER"),
		Location:     pulumi.String("us"),
		RepositoryId: pulumi.String(serviceName),
		Project:      pulumi.StringPtr(s.config.Project),
	})
	return err
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
	project := s.config.Project

	backendUrlMap, err := s.createCloudRunNEG(ctx)
	if err != nil {
		return err
	}

	if s.config.EnableTLS {
		err = s.createHTTPSProxy(ctx, backendUrlMap)
		if err != nil {
			return err
		}
	}

	if s.config.EnableHTTPForward {
		var httpProxyUrlMap pulumi.StringOutput
		if s.config.EnableHTTPSRedirect {
			// See:
			// https://github.com/terraform-google-modules/terraform-google-lb-http/blob/2a11956a2ed58fd60f1dde5a8277b8aeef70e6db/main.tf#L171
			httpRedirectUrlMap, err := compute.NewUrlMap(ctx, fmt.Sprintf("%s-https-redirect", serviceName), &compute.UrlMapArgs{
				Description: pulumi.String(fmt.Sprintf("URL map redirect from HTTP to HTTPS for %s", serviceName)),
				Project:     pulumi.String(project),
				DefaultUrlRedirect: compute.HttpRedirectActionArgs{
					HttpsRedirect:        pulumi.Bool(true),
					RedirectResponseCode: compute.HttpRedirectActionRedirectResponseCodeMovedPermanentlyDefault,
					StripQuery:           pulumi.Bool(false),
				},
			})
			if err != nil {
				return err
			}
			// point the HTTP proxy to the redirect map
			httpProxyUrlMap = httpRedirectUrlMap.SelfLink
		} else {
			// point the HTTP proxy to the default backend map
			httpProxyUrlMap = backendUrlMap.SelfLink
		}

		err := s.createHTTPProxy(ctx, httpProxyUrlMap)
		if err != nil {
			return err
		}
	}

	if s.config.EnableCloudArmor {
		err = s.createCloudArmorSecurityPolicy(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *serverlessStack) createCloudRunNEG(ctx *pulumi.Context) (*compute.UrlMap, error) {
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
		return nil, err
	}

	neg, err := compute.NewRegionNetworkEndpointGroup(ctx, fmt.Sprintf("%s-default", serviceName), &compute.RegionNetworkEndpointGroupArgs{
		Description:         pulumi.String(fmt.Sprintf("NEG to route LB traffic to %s", serviceName)),
		Project:             pulumi.String(project),
		Region:              pulumi.String(region),
		NetworkEndpointType: compute.RegionNetworkEndpointGroupNetworkEndpointTypeServerless,
		CloudRun: &compute.NetworkEndpointGroupCloudRunArgs{
			Service: pulumi.String(serviceName),
		},
	})
	if err != nil {
		return nil, err
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
		return nil, err
	}

	// TODO create compute address if enabled
	defaultBackendUrlMap, err := compute.NewUrlMap(ctx, fmt.Sprintf("%s-default", serviceName), &compute.UrlMapArgs{
		Description:    pulumi.String(fmt.Sprintf("URL map to LB traffic for %s", serviceName)),
		Project:        pulumi.String(project),
		DefaultService: service.SelfLink,
	})
	return defaultBackendUrlMap, err
}

func (s *serverlessStack) createHTTPProxy(ctx *pulumi.Context, httpProxyUrlMap pulumi.StringOutput) error {
	serviceName := s.config.ServiceName
	project := s.config.Project
	httpProxy, err := compute.NewTargetHttpProxy(ctx, fmt.Sprintf("%s-http", serviceName), &compute.TargetHttpProxyArgs{
		Description: pulumi.String(fmt.Sprintf("proxy to LB traffic for %s", serviceName)),
		Project:     pulumi.String(project),
		UrlMap:      httpProxyUrlMap,
	})
	if err != nil {
		return err
	}

	_, err = compute.NewGlobalForwardingRule(ctx, fmt.Sprintf("%s-http", serviceName), &compute.GlobalForwardingRuleArgs{
		Description:         pulumi.String(fmt.Sprintf("HTTP forwarding rule to LB traffic for %s", serviceName)),
		Project:             pulumi.String(project),
		PortRange:           pulumi.String("80"),
		NetworkTier:         compute.GlobalForwardingRuleNetworkTierPremium,
		LoadBalancingScheme: compute.GlobalForwardingRuleLoadBalancingSchemeExternal,
		Target:              httpProxy.SelfLink,
	})
	return err
}

func (s *serverlessStack) createHTTPSProxy(ctx *pulumi.Context, urlMap *compute.UrlMap) error {
	serviceName := s.config.ServiceName
	project := s.config.Project

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
		Description:         pulumi.String(fmt.Sprintf("HTTPS forwarding rule to LB traffic for %s", serviceName)),
		Project:             pulumi.String(project),
		PortRange:           pulumi.String("443"),
		NetworkTier:         compute.GlobalForwardingRuleNetworkTierPremium,
		LoadBalancingScheme: compute.GlobalForwardingRuleLoadBalancingSchemeExternal,
		Target:              httpsProxy.SelfLink,
	})
	return err
}

func (s *serverlessStack) createCloudArmorSecurityPolicy(ctx *pulumi.Context) error {
	project := s.config.Project
	serviceName := s.config.ServiceName

	// See: https://cloud.google.com/armor/docs/waf-rules
	var preconfiguredRules compute.SecurityPolicyRuleArray
	for _, rule := range []string{
		"sqli-v33-stable",
		"xss-v33-stable",
		"lfi-v33-stable",
		"rfi-v33-stable",
		"rce-v33-stable",
		"methodenforcement-v33-stable",
		"scannerdetection-v33-stable",
		"protocolattack-v33-stable",
		"sessionfixation-v33-stable",
		"nodejs-v33-stable",
	} {
		preconfiguredWafRule := fmt.Sprintf("evaluatePreconfiguredWaf('%s', {'sensitivity': 1})", rule)
		preconfiguredRules = append(preconfiguredRules, &compute.SecurityPolicyRuleArgs{
			Description: pulumi.String(fmt.Sprintf("preconfigured waf rule %s", rule)),
			Match: &compute.SecurityPolicyRuleMatcherArgs{
				Expr: &compute.ExprArgs{
					Expression: pulumi.String(preconfiguredWafRule),
				},
			},
		})
	}

	// TODO add rate limiting rules

	// TODO add named IP preconfigured rules

	_, err := compute.NewSecurityPolicy(ctx, serviceName, &compute.SecurityPolicyArgs{
		Description: pulumi.String(fmt.Sprintf("cloud armor security policy for %s", serviceName)),
		Project:     pulumi.String(project),
		Rules:       preconfiguredRules,
	})
	return err
}
