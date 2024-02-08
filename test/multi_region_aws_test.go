package multiregionaws

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"multiregiontests/internal/helpers"

	"github.com/camunda/zeebe/clients/go/v8/pkg/zbc"
	"github.com/gruntwork-io/go-commons/files"
	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/random"
	"github.com/gruntwork-io/terratest/modules/terraform"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	remoteChartSource  = "https://helm.camunda.io"
	remoteChartName    = "camunda/camunda-platform"
	remoteChartVersion = "8.3.5"

	terraformDir        = "./resources/aws/2-region/terraform"
	kubeConfigPrimary   = "./kubeconfig-london"
	kubeConfigSecondary = "./kubeconfig-paris"
	k8sManifests        = "./resources/aws/2-region/kubernetes"
)

var primary helpers.Cluster
var secondary helpers.Cluster

func TestSetupTerraform(t *testing.T) {
	t.Logf("[SETUP] Hello 👋!")
	terraformOptions := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: terraformDir,
		NoColor:      true,
	})

	terraform.InitAndApply(t, terraformOptions)

	cmd := exec.Command("aws", "eks", "--region", "eu-west-3", "update-kubeconfig", "--name", "lars-paris", "--profile", "infex", "--kubeconfig", "kubeconfig-paris")

	_, err := cmd.Output()
	if err != nil {
		fmt.Println("could not run command: ", err)
	}

	files.FileExists("kubeconfig-paris")

	cmd2 := exec.Command("aws", "eks", "--region", "eu-west-2", "update-kubeconfig", "--name", "lars-london", "--profile", "infex", "--kubeconfig", "kubeconfig-london")

	_, err2 := cmd2.Output()
	if err2 != nil {
		fmt.Println("could not run command: ", err2)
	}

	files.FileExists("kubeconfig-london")
}

func TestTeardownTerraform(t *testing.T) {
	t.Logf("[TEARDOWN] Bye, bye 🖖!")

	terraformOptions := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: terraformDir,
		NoColor:      true,
	})
	terraform.Destroy(t, terraformOptions)

	defer os.Remove("kubeconfig-paris")
	defer os.Remove("kubeconfig-london")

}

func Test2RegionAWSEKS(t *testing.T) {
	t.Parallel()

	// For CI run it separately
	// go test --count=1 -v -timeout 120m ../test -run TestSetupTerraform
	// go test --count=1 -v -timeout 120m ../test -run Test2RegionAWSEKS
	// go test --count=1 -v -timeout 120m ../test -run TestTearDownTerraform
	// setupTerraform(t)
	// defer teardownTerraform(t)

	for _, testFuncs := range []struct {
		name  string
		tfunc func(*testing.T)
	}{
		{"TestInitKubernetesHelpers", initKubernetesHelpers},
		// {"TestClusterReadyCheck", clusterReadyCheck},
		// {"TestCrossClusterCommunication", testCrossClusterCommunication},
		{"TestApplyDnsChaining", applyDnsChaining},
		{"TestCoreDNSReload", testCoreDNSReload},
		{"TestCrossClusterCommunicationWithDNS", testCrossClusterCommunicationWithDNS},
		// {"TestDeployC8Helm", deployC8Helm},
		// {"TestCheckC8RunningProperly", checkC8RunningProperly},
		// {"TestTeardownAllC8Helm", teardownAllC8Helm},
		{"TestCleanupKubernetes", cleanupKubernetes},
	} {
		t.Run(testFuncs.name, testFuncs.tfunc)
	}
}

func clusterReadyCheck(t *testing.T) {
	statusPrimary := helpers.WaitForCluster(primary.Region, primary.ClusterName)
	statusSecondary := helpers.WaitForCluster(secondary.Region, secondary.ClusterName)

	require.Equal(t, "ACTIVE", statusPrimary)
	require.Equal(t, "ACTIVE", statusSecondary)

	helpers.WaitForNodeGroup(primary.Region, primary.ClusterName, "services")
	helpers.WaitForNodeGroup(secondary.Region, secondary.ClusterName, "services")
}

// TODO: clean up resources after entire testing is done
func dnsChaining(t *testing.T, source, target k8s.KubectlOptions, namespaceSuffix string) {

	t.Logf(fmt.Sprintf("[DNS CHAINING] applying from source %s to configure target %s", source.ContextName, target.ContextName))

	kubeResourcePath := fmt.Sprintf("%s/%s", k8sManifests, "internal-dns-lb.yml")

	k8s.KubectlApply(t, &source, kubeResourcePath)

	k8s.WaitUntilServiceAvailable(t, &source, "internal-dns-lb", 15, 6*time.Second)

	host := k8s.GetService(t, &source, "internal-dns-lb")

	hostName := strings.Split(host.Status.LoadBalancer.Ingress[0].Hostname, ".")

	hostName = strings.Split(hostName[0], "-")

	description := fmt.Sprintf("ELB net/%s/%s", hostName[0], hostName[1])

	require.NotEmpty(t, description)

	region := primary.Region

	if namespaceSuffix == "secondary" {
		region = secondary.Region
	}

	privateIPs := helpers.GetPrivateIPsForInternalLB(region, description)

	require.NotEmpty(t, privateIPs)
	require.Greater(t, len(privateIPs), 1)

	configMap := k8s.GetConfigMap(t, &target, "coredns")

	require.Equal(t, configMap.Name, "coredns")

	filePath := "./resources/aws/2-region/kubernetes/coredns.yml"
	content, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		return
	}

	// Convert byte slice to string
	fileContent := string(content)

	// Define the template and replacement string
	template := "PLACEHOLDER"
	replacement := fmt.Sprintf(`
    camunda-%s.svc.cluster.local:53 {
        errors
        cache 30
        forward . %s {
            force_tcp
        }
    }`, namespaceSuffix, strings.Join(privateIPs, " "))

	// Replace the template with the replacement string
	modifiedContent := strings.Replace(fileContent, template, replacement, -1)

	// Write the modified content back to the file
	err = os.WriteFile(filePath, []byte(modifiedContent), 0644)
	if err != nil {
		fmt.Printf("Error writing file: %v\n", err)
		return
	}

	k8s.KubectlApply(t, &target, filePath)

	err = os.WriteFile(filePath, []byte(fileContent), 0644)
	if err != nil {
		fmt.Printf("Error writing file: %v\n", err)
		return
	}

}

func applyDnsChaining(t *testing.T) {
	dnsChaining(t, primary.KubectlSystem, secondary.KubectlSystem, "primary")
	dnsChaining(t, secondary.KubectlSystem, primary.KubectlSystem, "secondary")
}

func crossClusterCommunication(t *testing.T, namespace string) {
	kubeResourcePath := fmt.Sprintf("%s/%s", k8sManifests, "nginx.yml")

	optionsPrimary := primary.KubectlNamespace
	optionsSecondary := secondary.KubectlNamespace

	defer k8s.KubectlDelete(t, &optionsPrimary, kubeResourcePath)
	defer k8s.KubectlDelete(t, &optionsSecondary, kubeResourcePath)

	k8s.KubectlApply(t, &optionsPrimary, kubeResourcePath)
	k8s.KubectlApply(t, &optionsSecondary, kubeResourcePath)

	k8s.WaitUntilServiceAvailable(t, &optionsPrimary, "sample-nginx-peer", 10, 5*time.Second)
	k8s.WaitUntilServiceAvailable(t, &optionsSecondary, "sample-nginx-peer", 10, 5*time.Second)

	k8s.WaitUntilPodAvailable(t, &optionsPrimary, "sample-nginx", 10, 5*time.Second)
	k8s.WaitUntilPodAvailable(t, &optionsSecondary, "sample-nginx", 10, 5*time.Second)

	podPrimary := k8s.GetPod(t, &optionsPrimary, "sample-nginx")
	podPrimaryIP := podPrimary.Status.PodIP
	require.NotEmpty(t, podPrimaryIP)

	podSecondary := k8s.GetPod(t, &optionsSecondary, "sample-nginx")
	podSecondaryIP := podSecondary.Status.PodIP
	require.NotEmpty(t, podSecondaryIP)

	if namespace == "DNS" {
		for i := 0; i < 6; i++ {
			outputPrimary, errPrimary := k8s.RunKubectlAndGetOutputE(t, &optionsPrimary, "exec", podPrimary.Name, "--", "curl", "--max-time", "15", "sample-nginx.sample-nginx-peer.camunda-secondary.svc.cluster.local")
			outputSecondary, errSecondary := k8s.RunKubectlAndGetOutputE(t, &optionsSecondary, "exec", podSecondary.Name, "--", "curl", "--max-time", "15", "sample-nginx.sample-nginx-peer.camunda-primary.svc.cluster.local")
			if errPrimary != nil || errSecondary != nil {
				fmt.Println("Error: ", errPrimary)
				fmt.Println("Error: ", errSecondary)
				time.Sleep(15 * time.Second)
			}

			if outputPrimary != "" && outputSecondary != "" {
				fmt.Println(outputPrimary)
				fmt.Println(outputSecondary)
				break
			}
		}
	} else {
		k8s.RunKubectl(t, &optionsPrimary, "exec", podPrimary.Name, "--", "curl", "--max-time", "15", podSecondaryIP)
		k8s.RunKubectl(t, &optionsSecondary, "exec", podSecondary.Name, "--", "curl", "--max-time", "15", podPrimaryIP)
	}
}

func testCrossClusterCommunication(t *testing.T) {
	crossClusterCommunication(t, "withoutDNS")
}

func testCrossClusterCommunicationWithDNS(t *testing.T) {
	crossClusterCommunication(t, "DNS")
}

func testCoreDNSReload(t *testing.T) {
	checkCoreDNSReload(t, &primary.KubectlSystem)
	checkCoreDNSReload(t, &secondary.KubectlSystem)
}

func checkCoreDNSReload(t *testing.T, kubectlOptions *k8s.KubectlOptions) {
	pods := k8s.ListPods(t, kubectlOptions, metav1.ListOptions{LabelSelector: "k8s-app=kube-dns"})

	for _, pod := range pods {
		for i := 0; i < 8; i++ {
			logs := k8s.GetPodLogs(t, kubectlOptions, &pod, "coredns")

			if !strings.Contains(logs, "Reloading complete") {
				time.Sleep(15 * time.Second)
			} else {
				break
			}
		}
	}
}

func deployC8Helm(t *testing.T) {

	zeebeContactPoints := ""

	for i := 0; i < 4; i++ {
		zeebeContactPoints += fmt.Sprintf("camunda-zeebe-%s.camunda-zeebe.camunda-primary.svc.cluster.local:26502,", strconv.Itoa((i)))
		zeebeContactPoints += fmt.Sprintf("camunda-zeebe-%s.camunda-zeebe.camunda-secondary.svc.cluster.local:26502,", strconv.Itoa((i)))
	}

	zeebeContactPoints = zeebeContactPoints[:len(zeebeContactPoints)-1]

	filePath := "./resources/aws/2-region/kubernetes/camunda-values.yml"
	content, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		return
	}

	// Convert byte slice to string
	fileContent := string(content)

	// Define the template and replacement string
	template := "PLACEHOLDER"

	// Replace the template with the replacement string
	modifiedContent := strings.Replace(fileContent, template, zeebeContactPoints, -1)

	// Write the modified content back to the file
	err = os.WriteFile(filePath, []byte(modifiedContent), 0644)
	if err != nil {
		fmt.Printf("Error writing file: %v\n", err)
		return
	}

	helmOptionsPrimary := &helm.Options{
		KubectlOptions: &primary.KubectlNamespace,
		Version:        remoteChartVersion,
		ValuesFiles:    []string{"./resources/aws/2-region/kubernetes/camunda-values.yml", "./resources/aws/2-region/kubernetes/region0/camunda-values.yml"},
	}

	helmOptionsSecondary := &helm.Options{
		KubectlOptions: &secondary.KubectlNamespace,
		Version:        remoteChartVersion,
		ValuesFiles:    []string{"./resources/aws/2-region/kubernetes/camunda-values.yml", "./resources/aws/2-region/kubernetes/region1/camunda-values.yml"},
	}

	// TODO: move to separate teardown function
	// defer k8s.DeleteNamespace(t, kubectlOptions, namespaceName)
	// k8s.CreateNamespace(t, optionsPrimary, "camunda-primary")

	uniqueName := strings.ToLower(fmt.Sprintf("terratest-%s", random.UniqueId()))

	// TODO: move to separate teardown function
	// defer helm.RemoveRepo(t, options, uniqueName)
	helm.AddRepo(t, helmOptionsPrimary, uniqueName, remoteChartSource)
	helm.AddRepo(t, helmOptionsSecondary, uniqueName, remoteChartSource)

	helmChart := remoteChartName

	releaseName := "camunda"

	// TODO: move to separate teardown function
	// defer helm.Delete(t, options, releaseName, true)

	helm.Install(t, helmOptionsPrimary, helmChart, releaseName)
	helm.Install(t, helmOptionsSecondary, helmChart, releaseName)

	// 20 times with 15 seconds sleep = 5 minutes
	k8s.WaitUntilDeploymentAvailable(t, &primary.KubectlNamespace, "camunda-connectors", 20, 15*time.Second)
	k8s.WaitUntilDeploymentAvailable(t, &primary.KubectlNamespace, "camunda-operate", 20, 15*time.Second)
	k8s.WaitUntilDeploymentAvailable(t, &primary.KubectlNamespace, "camunda-tasklist", 20, 15*time.Second)
	k8s.WaitUntilDeploymentAvailable(t, &primary.KubectlNamespace, "camunda-zeebe-gateway", 20, 15*time.Second)

	// no functions for Statefulsets yet, fallback to pods
	k8s.WaitUntilPodAvailable(t, &primary.KubectlNamespace, "camunda-elasticsearch-master-0", 20, 15*time.Second)
	k8s.WaitUntilPodAvailable(t, &primary.KubectlNamespace, "camunda-elasticsearch-master-1", 20, 15*time.Second)
	k8s.WaitUntilPodAvailable(t, &primary.KubectlNamespace, "camunda-zeebe-0", 20, 15*time.Second)
	k8s.WaitUntilPodAvailable(t, &primary.KubectlNamespace, "camunda-zeebe-1", 20, 15*time.Second)
	k8s.WaitUntilPodAvailable(t, &primary.KubectlNamespace, "camunda-zeebe-2", 20, 15*time.Second)
	k8s.WaitUntilPodAvailable(t, &primary.KubectlNamespace, "camunda-zeebe-3", 20, 15*time.Second)

	// 20 times with 15 seconds sleep = 5 minutes
	k8s.WaitUntilDeploymentAvailable(t, &secondary.KubectlNamespace, "camunda-connectors", 20, 15*time.Second)
	k8s.WaitUntilDeploymentAvailable(t, &secondary.KubectlNamespace, "camunda-operate", 20, 15*time.Second)
	k8s.WaitUntilDeploymentAvailable(t, &secondary.KubectlNamespace, "camunda-tasklist", 20, 15*time.Second)
	k8s.WaitUntilDeploymentAvailable(t, &secondary.KubectlNamespace, "camunda-zeebe-gateway", 20, 15*time.Second)

	// no functions for Statefulsets yet, fallback to pods
	k8s.WaitUntilPodAvailable(t, &secondary.KubectlNamespace, "camunda-elasticsearch-master-0", 20, 15*time.Second)
	k8s.WaitUntilPodAvailable(t, &secondary.KubectlNamespace, "camunda-elasticsearch-master-1", 20, 15*time.Second)
	k8s.WaitUntilPodAvailable(t, &secondary.KubectlNamespace, "camunda-zeebe-0", 20, 15*time.Second)
	k8s.WaitUntilPodAvailable(t, &secondary.KubectlNamespace, "camunda-zeebe-1", 20, 15*time.Second)
	k8s.WaitUntilPodAvailable(t, &secondary.KubectlNamespace, "camunda-zeebe-2", 20, 15*time.Second)
	k8s.WaitUntilPodAvailable(t, &secondary.KubectlNamespace, "camunda-zeebe-3", 20, 15*time.Second)

	err = os.WriteFile(filePath, []byte(fileContent), 0644)
	if err != nil {
		fmt.Printf("Error writing file: %v\n", err)
		return
	}

}

func checkC8RunningProperly(t *testing.T) {

	service := k8s.GetService(t, &primary.KubectlNamespace, "camunda-zeebe-gateway")
	require.Equal(t, service.Name, "camunda-zeebe-gateway")

	tunnel := k8s.NewTunnel(&primary.KubectlNamespace, k8s.ResourceTypeService, "camunda-zeebe-gateway", 0, 26500)
	defer tunnel.Close()
	tunnel.ForwardPort(t)

	client, err := zbc.NewClient(&zbc.ClientConfig{
		GatewayAddress:         tunnel.Endpoint(),
		UsePlaintextConnection: true,
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	defer client.Close()

	// Get the topology of the Zeebe cluster
	topology, err := client.NewTopologyCommand().Send(context.Background())
	if err != nil {
		log.Fatalf("Failed to get topology: %v", err)
	}

	require.Equal(t, 8, len(topology.Brokers))

	primaryCount := 0
	secondaryCount := 0

	fmt.Println("Cluster status:")
	for _, broker := range topology.Brokers {
		if strings.Contains(broker.Host, "camunda-primary") {
			primaryCount++
		} else if strings.Contains(broker.Host, "camunda-secondary") {
			secondaryCount++
		}
		fmt.Printf("Broker ID: %d, Address: %s, Partitions: %v\n", broker.NodeId, broker.Host, broker.Partitions)
	}

	require.Equal(t, 4, primaryCount)
	require.Equal(t, 4, secondaryCount)
}

func teardownAllC8Helm(t *testing.T) {
	teardownC8Helm(t, &primary.KubectlNamespace)
	teardownC8Helm(t, &secondary.KubectlNamespace)

}

func teardownC8Helm(t *testing.T, kubectlOptions *k8s.KubectlOptions) {
	helmOptions := &helm.Options{
		KubectlOptions: kubectlOptions,
	}

	helm.Delete(t, helmOptions, "camunda", true)

	pvcs := k8s.ListPersistentVolumeClaims(t, kubectlOptions, metav1.ListOptions{})

	for _, pvc := range pvcs {
		k8s.RunKubectl(t, kubectlOptions, "delete", "pvc", pvc.Name)
	}

	pvs := k8s.ListPersistentVolumes(t, kubectlOptions, metav1.ListOptions{})

	for _, pv := range pvs {
		k8s.RunKubectl(t, kubectlOptions, "delete", "pv", pv.Name)
	}

}

func initKubernetesHelpers(t *testing.T) {
	primary = helpers.Cluster{
		Region:           "eu-west-2",
		ClusterName:      "lars-london",
		KubectlNamespace: *k8s.NewKubectlOptions("", kubeConfigPrimary, "camunda-primary"),
		KubectlSystem:    *k8s.NewKubectlOptions("", kubeConfigPrimary, "kube-system"),
	}
	secondary = helpers.Cluster{
		Region:           "eu-west-3",
		ClusterName:      "lars-paris",
		KubectlNamespace: *k8s.NewKubectlOptions("", kubeConfigSecondary, "camunda-secondary"),
		KubectlSystem:    *k8s.NewKubectlOptions("", kubeConfigSecondary, "kube-system"),
	}

	k8s.CreateNamespace(t, &primary.KubectlNamespace, "camunda-primary")
	k8s.CreateNamespace(t, &secondary.KubectlNamespace, "camunda-secondary")
}

func cleanupKubernetes(t *testing.T) {
	k8s.DeleteNamespace(t, &primary.KubectlNamespace, "camunda-primary")
	k8s.DeleteNamespace(t, &secondary.KubectlNamespace, "camunda-secondary")

	k8s.RunKubectl(t, &primary.KubectlSystem, "delete", "service", "internal-dns-lb")
	k8s.RunKubectl(t, &secondary.KubectlSystem, "delete", "service", "internal-dns-lb")
}
