package test

import (
	"fmt"
	"os"
	"testing"
	"time"

	http_helper "github.com/gruntwork-io/terratest/modules/http-helper"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/stretchr/testify/require"
)

const (
	remoteChartSource  = "https://helm.camunda.io"
	remoteChartName    = "camunda/camunda-platform"
	remoteChartVersion = "8.3.5"
)

// An example of how to test the simple Terraform module in examples/terraform-basic-example using Terratest.
func TestTerraformBasicExample(t *testing.T) {
	t.Parallel()

	terraformOptions := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		// website::tag::1::Set the path to the Terraform code that will be tested.
		// The path to where our Terraform code is located
		TerraformDir: "../examples/",

		// Disable colors in Terraform commands so its easier to parse stdout/stderr
		NoColor: true,
	})

	// website::tag::4::Clean up resources with "terraform destroy". Using "defer" runs the command at the end of the test, whether the test succeeds or fails.
	// At the end of the test, run `terraform destroy` to clean up any resources that were created
	defer terraform.Destroy(t, terraformOptions)

	// website::tag::2::Run "terraform init" and "terraform apply".
	// This will run `terraform init` and `terraform apply` and fail the test if there are any errors
	terraform.InitAndApply(t, terraformOptions)

	kubeResourcePath := "../examples/hello-world-deployment.yml"

	tmpConfigPath := "../examples/kubeconfig"
	defer os.Remove(tmpConfigPath)

	options := k8s.NewKubectlOptions("", tmpConfigPath, "default")

	defer k8s.KubectlDelete(t, options, kubeResourcePath)

	k8s.KubectlApply(t, options, kubeResourcePath)

	k8s.WaitUntilDeploymentAvailable(t, options, "hello-world-deployment", 15, 1*time.Second)
	k8s.WaitUntilServiceAvailable(t, options, "hello-world-service", 10, 1*time.Second)
	service := k8s.GetService(t, options, "hello-world-service")
	require.Equal(t, service.Name, "hello-world-service")

	tunnel := k8s.NewTunnel(options, k8s.ResourceTypeService, "hello-world-service", 0, 5000)
	defer tunnel.Close()
	tunnel.ForwardPort(t)

	url := fmt.Sprintf("http://%s", tunnel.Endpoint())

	// Make an HTTP request to the URL and make sure it returns a 200 OK with the body "Hello, World".
	http_helper.HttpGetWithRetry(t, url, nil, 200, "Hello world!", 30, 3*time.Second)
}

// currently requires the cluster to exist already. Still have to figure out ordering via golang subtests
// func TestC8Helm(t *testing.T) {
// 	t.Parallel()

// 	namespaceName := fmt.Sprintf(
// 		"%s-%s",
// 		strings.ToLower(t.Name()),
// 		strings.ToLower(random.UniqueId()),
// 	)

// 	tmpConfigPath := "../examples/kubeconfig"

// 	kubectlOptions := k8s.NewKubectlOptions("", tmpConfigPath, namespaceName)

// 	options := &helm.Options{
// 		KubectlOptions: kubectlOptions,
// 		Version:        remoteChartVersion,
// 	}

// 	defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)
// 	k8s.CreateNamespace(t, kubectlOptions, namespaceName)

// 	uniqueName := strings.ToLower(fmt.Sprintf("terratest-%s", random.UniqueId()))
// 	defer helm.RemoveRepo(t, options, uniqueName)
// 	helm.AddRepo(t, options, uniqueName, remoteChartSource)

// 	// helmChart := fmt.Sprintf("%s/%s", uniqueName, remoteChartName)

// 	helmChart := remoteChartName

// 	releaseName := "camunda"

// 	defer helm.Delete(t, options, releaseName, true)

// 	// Fix chart version and retry install
// 	options.Version = remoteChartVersion

// 	helm.Install(t, options, helmChart, releaseName)

// 	k8s.WaitUntilDeploymentAvailable(t, kubectlOptions, "camunda-operate", 30, 3*time.Second)

// }
