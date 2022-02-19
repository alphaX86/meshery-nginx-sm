package main

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/layer5io/meshery-nginx/nginx"
	"github.com/layer5io/meshery-nginx/nginx/oam"
	"github.com/layer5io/meshkit/logger"
	"github.com/layer5io/meshkit/utils/manifests"

	// "github.com/layer5io/meshkit/tracing"
	"github.com/layer5io/meshery-adapter-library/adapter"
	"github.com/layer5io/meshery-adapter-library/api/grpc"
	configprovider "github.com/layer5io/meshery-adapter-library/config/provider"
	"github.com/layer5io/meshery-nginx/internal/config"
	mesherykube "github.com/layer5io/meshkit/utils/kubernetes"
	smp "github.com/layer5io/service-mesh-performance/spec"
)

var (
	serviceName = "nginx-adapter"
	version     = "edge"
	gitsha      = "none"
)

// creates the ~/.meshery directory
func init() {
	err := os.MkdirAll(path.Join(config.RootPath(), "bin"), 0750)
	if err != nil {
		fmt.Println(err)
		os.Exit(0)
	}
}

// main is the entrypoint of the adaptor
func main() {

	// Initialize Logger instance
	log, err := logger.New(serviceName, logger.Options{
		Format: logger.SyslogLogFormat,
	})
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = os.Setenv("KUBECONFIG", path.Join(
		config.KubeConfig[configprovider.FilePath],
		fmt.Sprintf("%s.%s", config.KubeConfig[configprovider.FileName], config.KubeConfig[configprovider.FileType])),
	)
	if err != nil {
		// Fail silently
		log.Warn(err)
	}

	// Initialize application specific configs and dependencies
	// App and request config
	cfg, err := config.New(configprovider.ViperKey)
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}

	service := &grpc.Service{}
	err = cfg.GetObject(adapter.ServerKey, service)
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}

	kubeconfigHandler, err := config.NewKubeconfigBuilder(configprovider.ViperKey)
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}

	// // Initialize Tracing instance
	// tracer, err := tracing.New(service.Name, service.TraceURL)
	// if err != nil {
	// 	log.Err("Tracing Init Failed", err.Error())
	// 	os.Exit(1)
	// }

	// Initialize Handler intance
	handler := nginx.New(cfg, log, kubeconfigHandler)
	handler = adapter.AddLogger(log, handler)

	service.Handler = handler
	service.Channel = make(chan interface{}, 10)
	service.StartedAt = time.Now()
	service.Version = version
	service.GitSHA = gitsha

	go registerCapabilities(service.Port, log)        //Registering static capabilities
	go registerDynamicCapabilities(service.Port, log) //Registering latest capabilities periodically

	// Server Initialization
	log.Info("Adapter Listening at port: ", service.Port)
	err = grpc.Start(service, nil)
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}
}

func mesheryServerAddress() string {
	meshReg := os.Getenv("MESHERY_SERVER")

	if meshReg != "" {
		if strings.HasPrefix(meshReg, "http") {
			return meshReg
		}

		return "http://" + meshReg
	}

	return "http://localhost:9081"
}

func serviceAddress() string {
	svcAddr := os.Getenv("SERVICE_ADDR")

	if svcAddr != "" {
		return svcAddr
	}

	return "localhost"
}

func registerCapabilities(port string, log logger.Handler) {
	// Register workloads
	if err := oam.RegisterWorkloads(mesheryServerAddress(), serviceAddress()+":"+port); err != nil {
		log.Info(err.Error())
	}

	// Register traits
	if err := oam.RegisterTraits(mesheryServerAddress(), serviceAddress()+":"+port); err != nil {
		log.Info(err.Error())
	}
}

func registerDynamicCapabilities(port string, log logger.Handler) {
	registerWorkloads(port, log)
	//Start the ticker
	const reRegisterAfter = 24
	ticker := time.NewTicker(reRegisterAfter * time.Hour)
	for {
		<-ticker.C
		registerWorkloads(port, log)
	}

}

const (
	repo  = "https://helm.nginx.com/stable"
	chart = "nginx-service-mesh"
)

func registerWorkloads(port string, log logger.Handler) {
	release, err := config.GetLatestReleases(1)
	if err != nil {
		log.Info("Could not get latest version")
		return
	}
	version := release[0].TagName
	log.Info("Registering latest workload components for version ", version)
	//removing v from the version number
	res := strings.Replace(version, "v", "", 1)

	//getting chart version
	chartVersion, err := mesherykube.HelmAppVersionToChartVersion(repo, chart, res)
	if err != nil {
		log.Info("Could not change the version string", err)
	}

	// Register workloads
	if err := adapter.RegisterWorkLoadsDynamically(mesheryServerAddress(), serviceAddress()+":"+port, &adapter.DynamicComponentsConfig{
		TimeoutInMinutes: 60,
		URL:              "https://github.com/nginxinc/helm-charts/blob/master/stable/nginx-service-mesh-" + chartVersion + ".tgz?raw=true",
		GenerationMethod: adapter.HelmCHARTS,
		Config: manifests.Config{
			Name:        smp.ServiceMesh_Type_name[int32(smp.ServiceMesh_NGINX_SERVICE_MESH)],
			MeshVersion: version,
			Filter: manifests.CrdFilter{
				RootFilter:    []string{"$[?(@.kind==\"CustomResourceDefinition\")]"},
				NameFilter:    []string{"$..[\"spec\"][\"names\"][\"kind\"]"},
				VersionFilter: []string{"$..spec.versions[0]", " --o-filter", "$[0]"},
				GroupFilter:   []string{"$..spec", " --o-filter", "$[]"},
				SpecFilter:    []string{"$..openAPIV3Schema.properties.spec", " --o-filter", "$[]"},
				ItrFilter:     []string{"$[?(@.spec.names.kind"},
				ItrSpecFilter: []string{"$[?(@.spec.names.kind"},
				VField:        "name",
				GField:        "group",
			},
		},
		Operation: config.NginxOperation,
	}); err != nil {
		log.Error(err)
		return
	}
	log.Info("Latest workload components successfully registered.")
}
