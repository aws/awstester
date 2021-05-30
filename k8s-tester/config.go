package k8s_tester

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	cloudwatch_agent "github.com/aws/aws-k8s-tester/k8s-tester/cloudwatch-agent"
	fluent_bit "github.com/aws/aws-k8s-tester/k8s-tester/fluent-bit"
	jobs_echo "github.com/aws/aws-k8s-tester/k8s-tester/jobs-echo"
	jobs_pi "github.com/aws/aws-k8s-tester/k8s-tester/jobs-pi"
	kubernetes_dashboard "github.com/aws/aws-k8s-tester/k8s-tester/kubernetes-dashboard"
	metrics_server "github.com/aws/aws-k8s-tester/k8s-tester/metrics-server"
	nlb_hello_world "github.com/aws/aws-k8s-tester/k8s-tester/nlb-hello-world"
	"github.com/aws/aws-k8s-tester/utils/log"
	"github.com/aws/aws-k8s-tester/utils/rand"
	utils_time "github.com/aws/aws-k8s-tester/utils/time"
	"github.com/mitchellh/colorstring"
	"sigs.k8s.io/yaml"
)

// Config defines k8s-tester configurations.
// The tester order is defined as https://github.com/aws/aws-k8s-tester/blob/v1.5.9/eksconfig/env.go.
// By default, it uses the environmental variables as https://github.com/aws/aws-k8s-tester/blob/v1.5.9/eksconfig/env.go.
// TODO: support https://github.com/onsi/ginkgo.
type Config struct {
	mu    *sync.RWMutex `json:"-"`
	Stopc chan struct{} `json:"-"`

	// Prompt is true to enable prompt mode.
	Prompt bool `json:"prompt"`

	// ClusterName is the Kubernetes cluster name.
	ClusterName string `json:"cluster_name"`

	// ConfigPath is the configuration file path.
	ConfigPath string `json:"config_path"`
	// LogColor is true to output logs in color.
	LogColor bool `json:"log_color"`
	// LogColorOverride is not empty to override "LogColor" setting.
	// If not empty, the automatic color check is not even run and use this value instead.
	// For instance, github action worker might not support color device,
	// thus exiting color check with the exit code 1.
	// Useful to output in color in HTML based log outputs (e.g., Prow).
	// Useful to skip terminal color check when there is no color device (e.g., Github action worker).
	LogColorOverride string `json:"log_color_override"`
	// LogLevel configures log level. Only supports debug, info, warn, error, panic, or fatal. Default 'info'.
	LogLevel string `json:"log-level"`
	// LogOutputs is a list of log outputs. Valid values are 'default', 'stderr', 'stdout', or file names.
	// Logs are appended to the existing file, if any.
	// Multiple values are accepted. If empty, it sets to 'default', which outputs to stderr.
	// See https://pkg.go.dev/go.uber.org/zap#Open and https://pkg.go.dev/go.uber.org/zap#Config for more details.
	LogOutputs []string `json:"log-outputs"`

	KubectlDownloadURL string `json:"kubectl-download-url"`
	KubectlPath        string `json:"kubectl_path"`
	KubeconfigPath     string `json:"kubeconfig_path"`
	KubeconfigContext  string `json:"kubeconfig_context"`

	// MinimumNodes is the minimum number of Kubernetes nodes required for installing this addon.
	MinimumNodes int `json:"minimum_nodes"`
	// TotalNodes is the total number of nodes from all node groups.
	TotalNodes int `json:"total_nodes" read-only:"true"`

	// The tester order is defined as https://github.com/aws/aws-k8s-tester/blob/v1.5.9/eksconfig/env.go.
	AddOnCloudwatchAgent     *cloudwatch_agent.Config     `json:"add_on_cloudwatch_agent"`
	AddOnMetricsServer       *metrics_server.Config       `json:"add_on_metrics_server"`
	AddOnFluentBit           *fluent_bit.Config           `json:"add_on_fluent_bit"`
	AddOnKubernetesDashboard *kubernetes_dashboard.Config `json:"add_on_kubernetes_dashboard"`

	AddOnNLBHelloWorld *nlb_hello_world.Config `json:"add_on_nlb_hello_world"`

	AddOnJobsPi       *jobs_pi.Config   `json:"add_on_jobs_pi"`
	AddOnJobsEcho     *jobs_echo.Config `json:"add_on_jobs_echo"`
	AddOnCronJobsEcho *jobs_echo.Config `json:"add_on_cron_jobs_echo"`
}

const DefaultMinimumNodes = 1

func NewDefault() *Config {
	name := fmt.Sprintf("k8s-%s-%s", utils_time.GetTS(10), rand.String(12))
	if v := os.Getenv(ENV_PREFIX + "CLUSTER_NAME"); v != "" {
		name = v
	}

	return &Config{
		mu: new(sync.RWMutex),

		Prompt:      true,
		ClusterName: name,

		LogColor:         true,
		LogColorOverride: "",
		LogLevel:         log.DefaultLogLevel,
		// default, stderr, stdout, or file name
		// log file named with cluster name will be added automatically
		LogOutputs: []string{"stderr"},

		// https://github.com/kubernetes/kubernetes/tags
		// https://kubernetes.io/docs/tasks/tools/install-kubectl/
		// https://docs.aws.amazon.com/eks/latest/userguide/install-kubectl.html
		KubectlPath:        "/tmp/kubectl-test-v1.21.0",
		KubectlDownloadURL: fmt.Sprintf("https://storage.googleapis.com/kubernetes-release/release/v1.21.0/bin/%s/%s/kubectl", runtime.GOOS, runtime.GOARCH),

		MinimumNodes: DefaultMinimumNodes,

		// The tester order is defined as https://github.com/aws/aws-k8s-tester/blob/v1.5.9/eksconfig/env.go.
		AddOnCloudwatchAgent:     cloudwatch_agent.NewDefault(),
		AddOnMetricsServer:       metrics_server.NewDefault(),
		AddOnFluentBit:           fluent_bit.NewDefault(),
		AddOnKubernetesDashboard: kubernetes_dashboard.NewDefault(),
		AddOnNLBHelloWorld:       nlb_hello_world.NewDefault(),
		AddOnJobsPi:              jobs_pi.NewDefault(),
		AddOnJobsEcho:            jobs_echo.NewDefault("Job"),
		AddOnCronJobsEcho:        jobs_echo.NewDefault("CronJob"),
	}
}

// ENV_PREFIX is the environment variable prefix.
const ENV_PREFIX = "K8S_TESTER_"

func Load(p string) (cfg *Config, err error) {
	var d []byte
	d, err = ioutil.ReadFile(p)
	if err != nil {
		return nil, err
	}
	cfg = new(Config)
	if err = yaml.Unmarshal(d, cfg, yaml.DisallowUnknownFields); err != nil {
		return nil, err
	}

	cfg.mu = new(sync.RWMutex)
	if cfg.ConfigPath != p {
		cfg.ConfigPath = p
	}

	var ap string
	ap, err = filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	cfg.ConfigPath = ap

	if serr := cfg.unsafeSync(); serr != nil {
		fmt.Fprintf(os.Stderr, "[WARN] failed to sync config files %v\n", serr)
	}

	return cfg, nil
}

// Sync writes the configuration file to disk.
func (cfg *Config) Sync() error {
	cfg.mu.Lock()
	defer cfg.mu.Unlock()
	return cfg.unsafeSync()
}

func (cfg *Config) unsafeSync() error {
	if cfg.ConfigPath == "" {
		return errors.New("empty config path")
	}

	if cfg.ConfigPath != "" && !filepath.IsAbs(cfg.ConfigPath) {
		p, err := filepath.Abs(cfg.ConfigPath)
		if err != nil {
			return fmt.Errorf("failed to 'filepath.Abs(%s)' %v", cfg.ConfigPath, err)
		}
		cfg.ConfigPath = p
	}
	if cfg.KubeconfigPath != "" && !filepath.IsAbs(cfg.KubeconfigPath) {
		p, err := filepath.Abs(cfg.KubeconfigPath)
		if err != nil {
			return fmt.Errorf("failed to 'filepath.Abs(%s)' %v", cfg.KubeconfigPath, err)
		}
		cfg.KubeconfigPath = p
	}

	d, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to 'yaml.Marshal' %v", err)
	}
	err = ioutil.WriteFile(cfg.ConfigPath, d, 0600)
	if err != nil {
		return fmt.Errorf("failed to write file %q (%v)", cfg.ConfigPath, err)
	}

	return nil
}

// UpdateFromEnvs updates fields from environmental variables.
// Empty values are ignored and do not overwrite fields with empty values.
// WARNING: The environmental variable value always overwrites current field
// values if there's a conflict.
func (cfg *Config) UpdateFromEnvs() (err error) {
	var vv interface{}
	vv, err = parseEnvs(ENV_PREFIX, cfg)
	if err != nil {
		return err
	}
	if av, ok := vv.(*Config); ok {
		cfg = av
	} else {
		return fmt.Errorf("expected *Config, got %T", vv)
	}

	// The tester order is defined as https://github.com/aws/aws-k8s-tester/blob/v1.5.9/eksconfig/env.go.
	vv, err = parseEnvs(ENV_PREFIX+cloudwatch_agent.Env()+"_", cfg.AddOnCloudwatchAgent)
	if err != nil {
		return err
	}
	if av, ok := vv.(*cloudwatch_agent.Config); ok {
		cfg.AddOnCloudwatchAgent = av
	} else {
		return fmt.Errorf("expected *cloudwatch_agent.Config, got %T", vv)
	}

	vv, err = parseEnvs(ENV_PREFIX+metrics_server.Env()+"_", cfg.AddOnMetricsServer)
	if err != nil {
		return err
	}
	if av, ok := vv.(*metrics_server.Config); ok {
		cfg.AddOnMetricsServer = av
	} else {
		return fmt.Errorf("expected *metrics_server.Config, got %T", vv)
	}

	vv, err = parseEnvs(ENV_PREFIX+fluent_bit.Env()+"_", cfg.AddOnFluentBit)
	if err != nil {
		return err
	}
	if av, ok := vv.(*fluent_bit.Config); ok {
		cfg.AddOnFluentBit = av
	} else {
		return fmt.Errorf("expected *fluent_bit.Config, got %T", vv)
	}

	vv, err = parseEnvs(ENV_PREFIX+kubernetes_dashboard.Env()+"_", cfg.AddOnKubernetesDashboard)
	if err != nil {
		return err
	}
	if av, ok := vv.(*kubernetes_dashboard.Config); ok {
		cfg.AddOnKubernetesDashboard = av
	} else {
		return fmt.Errorf("expected *kubernetes_dashboard.Config, got %T", vv)
	}

	vv, err = parseEnvs(ENV_PREFIX+nlb_hello_world.Env()+"_", cfg.AddOnNLBHelloWorld)
	if err != nil {
		return err
	}
	if av, ok := vv.(*nlb_hello_world.Config); ok {
		cfg.AddOnNLBHelloWorld = av
	} else {
		return fmt.Errorf("expected *nlb_hello_world.Config, got %T", vv)
	}

	vv, err = parseEnvs(ENV_PREFIX+jobs_pi.Env()+"_", cfg.AddOnJobsPi)
	if err != nil {
		return err
	}
	if av, ok := vv.(*jobs_pi.Config); ok {
		cfg.AddOnJobsPi = av
	} else {
		return fmt.Errorf("expected *jobs_pi.Config, got %T", vv)
	}

	vv, err = parseEnvs(ENV_PREFIX+jobs_echo.Env("Job")+"_", cfg.AddOnJobsEcho)
	if err != nil {
		return err
	}
	if av, ok := vv.(*jobs_echo.Config); ok {
		cfg.AddOnJobsEcho = av
	} else {
		return fmt.Errorf("expected *jobs_echo.Config, got %T", vv)
	}

	vv, err = parseEnvs(ENV_PREFIX+jobs_echo.Env("CronJob")+"_", cfg.AddOnCronJobsEcho)
	if err != nil {
		return err
	}
	if av, ok := vv.(*jobs_echo.Config); ok {
		cfg.AddOnCronJobsEcho = av
	} else {
		return fmt.Errorf("expected *jobs_echo.Config, got %T", vv)
	}

	return err
}

func parseEnvs(pfx string, addOn interface{}) (interface{}, error) {
	tp, vv := reflect.TypeOf(addOn).Elem(), reflect.ValueOf(addOn).Elem()
	for i := 0; i < tp.NumField(); i++ {
		jv := tp.Field(i).Tag.Get("json")
		if jv == "" {
			continue
		}
		jv = strings.Replace(jv, ",omitempty", "", -1)
		jv = strings.ToUpper(strings.Replace(jv, "-", "_", -1))
		env := pfx + jv
		sv := os.Getenv(env)
		if sv == "" {
			continue
		}
		if tp.Field(i).Tag.Get("read-only") == "true" { // error when read-only field is set for update
			return nil, fmt.Errorf("'%s=%s' is 'read-only' field; should not be set", env, sv)
		}
		fieldName := tp.Field(i).Name

		switch vv.Field(i).Type().Kind() {
		case reflect.String:
			vv.Field(i).SetString(sv)

		case reflect.Bool:
			bb, err := strconv.ParseBool(sv)
			if err != nil {
				return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
			}
			vv.Field(i).SetBool(bb)

		case reflect.Int, reflect.Int32, reflect.Int64:
			if vv.Field(i).Type().Name() == "Duration" {
				iv, err := time.ParseDuration(sv)
				if err != nil {
					return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
				}
				vv.Field(i).SetInt(int64(iv))
			} else {
				iv, err := strconv.ParseInt(sv, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
				}
				vv.Field(i).SetInt(iv)
			}

		case reflect.Uint, reflect.Uint32, reflect.Uint64:
			iv, err := strconv.ParseUint(sv, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
			}
			vv.Field(i).SetUint(iv)

		case reflect.Float32, reflect.Float64:
			fv, err := strconv.ParseFloat(sv, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
			}
			vv.Field(i).SetFloat(fv)

		case reflect.Slice: // only supports "[]string" for now
			ss := strings.Split(sv, ",")
			if len(ss) < 1 {
				continue
			}
			slice := reflect.MakeSlice(reflect.TypeOf([]string{}), len(ss), len(ss))
			for j := range ss {
				slice.Index(j).SetString(ss[j])
			}
			vv.Field(i).Set(slice)

		case reflect.Map:
			switch fieldName {
			case "Tags",
				"NodeSelector",
				"DeploymentNodeSelector",
				"DeploymentNodeSelector2048":
				vv.Field(i).Set(reflect.ValueOf(make(map[string]string)))
				mm := make(map[string]string)
				if err := json.Unmarshal([]byte(sv), &mm); err != nil {
					return nil, fmt.Errorf("failed to parse %q (field name %q, environmental variable key %q, error %v)", sv, fieldName, env, err)
				}
				vv.Field(i).Set(reflect.ValueOf(mm))

			default:
				return nil, fmt.Errorf("field %q not supported for reflect.Map", fieldName)
			}
		}
	}
	return addOn, nil
}

// Colorize prints colorized input, if color output is supported.
func (cfg *Config) Colorize(input string) string {
	colorize := colorstring.Colorize{
		Colors:  colorstring.DefaultColors,
		Disable: !cfg.LogColor,
		Reset:   true,
	}
	return colorize.Color(input)
}

// KubectlCommand returns the kubectl command.
func (cfg *Config) KubectlCommand() string {
	return fmt.Sprintf("%s --kubeconfig=%s", cfg.KubectlPath, cfg.KubeconfigPath)
}

// KubectlCommands returns the various kubectl commands.
func (cfg *Config) KubectlCommands() (s string) {
	if cfg.KubeconfigPath == "" {
		return ""
	}
	tpl := template.Must(template.New("kubectlTmpl").Parse(kubectlTmpl))
	buf := bytes.NewBuffer(nil)
	if err := tpl.Execute(buf, struct {
		KubeconfigPath string
		KubectlCommand string
	}{
		cfg.KubeconfigPath,
		cfg.KubectlCommand(),
	}); err != nil {
		return ""
	}
	return buf.String()
}

const kubectlTmpl = `
###########################
# kubectl commands
export KUBEcONFIG={{ .KubeconfigPath }}
export KUBECTL="{{ .KubectlCommand }}"

{{ .KubectlCommand }} version
{{ .KubectlCommand }} cluster-info
{{ .KubectlCommand }} get cs
{{ .KubectlCommand }} --namespace=kube-system get pods
{{ .KubectlCommand }} --namespace=kube-system get ds
{{ .KubectlCommand }} get pods
{{ .KubectlCommand }} get csr -o=yaml
{{ .KubectlCommand }} get nodes --show-labels -o=wide
{{ .KubectlCommand }} get nodes -o=wide
###########################
{{ end }}
`