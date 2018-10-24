package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/coreos/ignition/config/util"
	igntypes "github.com/coreos/ignition/config/v2_2/types"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/openshift/installer/pkg/asset"
	"github.com/openshift/installer/pkg/asset/ignition"
	"github.com/openshift/installer/pkg/asset/ignition/bootstrap/content"
	"github.com/openshift/installer/pkg/asset/installconfig"
	"github.com/openshift/installer/pkg/asset/kubeconfig"
	"github.com/openshift/installer/pkg/asset/manifests"
	"github.com/openshift/installer/pkg/asset/tls"
	"github.com/openshift/installer/pkg/types"
)

const (
	rootDir              = "/opt/tectonic"
	defaultReleaseImage  = "registry.svc.ci.openshift.org/openshift/origin-release:v4.0"
	bootstrapIgnFilename = "bootstrap.ign"
)

// bootstrapTemplateData is the data to use to replace values in bootstrap
// template files.
type bootstrapTemplateData struct {
	BootkubeImage       string
	ClusterDNSIP        string
	EtcdCertSignerImage string
	EtcdCluster         string
	EtcdctlImage        string
	ReleaseImage        string
}

// Bootstrap is an asset that generates the ignition config for bootstrap nodes.
type Bootstrap struct {
	Config *igntypes.Config
	File   *asset.File
}

var _ asset.WritableAsset = (*Bootstrap)(nil)

// Dependencies returns the assets on which the Bootstrap asset depends.
func (a *Bootstrap) Dependencies() []asset.Asset {
	return []asset.Asset{
		&installconfig.InstallConfig{},
		&tls.RootCA{},
		&tls.EtcdCA{},
		&tls.IngressCertKey{},
		&tls.KubeCA{},
		&tls.AggregatorCA{},
		&tls.ServiceServingCA{},
		&tls.ClusterAPIServerCertKey{},
		&tls.EtcdClientCertKey{},
		&tls.APIServerCertKey{},
		&tls.OpenshiftAPIServerCertKey{},
		&tls.APIServerProxyCertKey{},
		&tls.AdminCertKey{},
		&tls.KubeletCertKey{},
		&tls.MCSCertKey{},
		&tls.ServiceAccountKeyPair{},
		&kubeconfig.Admin{},
		&kubeconfig.Kubelet{},
		&manifests.Manifests{},
		&manifests.Tectonic{},
	}
}

// Generate generates the ignition config for the Bootstrap asset.
func (a *Bootstrap) Generate(dependencies asset.Parents) error {
	installConfig := &installconfig.InstallConfig{}
	dependencies.Get(installConfig)

	templateData, err := a.getTemplateData(installConfig.Config)
	if err != nil {
		return errors.Wrap(err, "failed to get bootstrap templates")
	}

	a.Config = &igntypes.Config{
		Ignition: igntypes.Ignition{
			Version: igntypes.MaxVersion.String(),
		},
	}

	a.addBootstrapFiles(dependencies)
	a.addBootkubeFiles(dependencies, templateData)
	a.addTemporaryBootkubeFiles(templateData)
	a.addTectonicFiles(dependencies)
	a.addTLSCertFiles(dependencies)

	a.Config.Systemd.Units = append(
		a.Config.Systemd.Units,
		igntypes.Unit{Name: "bootkube.service", Contents: content.BootkubeSystemdContents},
		igntypes.Unit{Name: "tectonic.service", Contents: content.TectonicSystemdContents},
		igntypes.Unit{Name: "progress.service", Contents: content.ReportSystemdContents, Enabled: util.BoolToPtr(true)},
		igntypes.Unit{Name: "kubelet.service", Contents: content.KubeletSystemdContents, Enabled: util.BoolToPtr(true)},
	)

	a.Config.Passwd.Users = append(
		a.Config.Passwd.Users,
		igntypes.PasswdUser{Name: "core", SSHAuthorizedKeys: []igntypes.SSHAuthorizedKey{igntypes.SSHAuthorizedKey(installConfig.Config.Admin.SSHKey)}},
	)

	data, err := json.Marshal(a.Config)
	if err != nil {
		return errors.Wrap(err, "failed to Marshal Ignition config")
	}
	a.File = &asset.File{
		Filename: bootstrapIgnFilename,
		Data:     data,
	}

	return nil
}

// Name returns the human-friendly name of the asset.
func (a *Bootstrap) Name() string {
	return "Bootstrap Ignition Config"
}

// Files returns the files generated by the asset.
func (a *Bootstrap) Files() []*asset.File {
	if a.File != nil {
		return []*asset.File{a.File}
	}
	return []*asset.File{}
}

// getTemplateData returns the data to use to execute bootstrap templates.
func (a *Bootstrap) getTemplateData(installConfig *types.InstallConfig) (*bootstrapTemplateData, error) {
	clusterDNSIP, err := installconfig.ClusterDNSIP(installConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get ClusterDNSIP from InstallConfig")
	}
	etcdEndpoints := make([]string, installConfig.MasterCount())
	for i := range etcdEndpoints {
		etcdEndpoints[i] = fmt.Sprintf("https://%s-etcd-%d.%s:2379", installConfig.ObjectMeta.Name, i, installConfig.BaseDomain)
	}

	releaseImage := defaultReleaseImage
	if ri, ok := os.LookupEnv("OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE"); ok && ri != "" {
		log.Warn("Found override for ReleaseImage. Please be warned, this is not advised")
		releaseImage = ri
	}

	return &bootstrapTemplateData{
		ClusterDNSIP:        clusterDNSIP,
		EtcdCertSignerImage: "quay.io/coreos/kube-etcd-signer-server:678cc8e6841e2121ebfdb6e2db568fce290b67d6",
		EtcdctlImage:        "quay.io/coreos/etcd:v3.2.14",
		BootkubeImage:       "quay.io/coreos/bootkube:v0.10.0",
		ReleaseImage:        releaseImage,
		EtcdCluster:         strings.Join(etcdEndpoints, ","),
	}, nil
}

func (a *Bootstrap) addBootstrapFiles(dependencies asset.Parents) {
	kubeletKubeconfig := &kubeconfig.Kubelet{}
	dependencies.Get(kubeletKubeconfig)

	a.Config.Storage.Files = append(
		a.Config.Storage.Files,
		ignition.FileFromBytes("/etc/kubernetes/kubeconfig", 0600, kubeletKubeconfig.Files()[0].Data),
	)
	a.Config.Storage.Files = append(
		a.Config.Storage.Files,
		ignition.FileFromString("/usr/local/bin/report-progress.sh", 0555, content.ReportShFileContents),
	)
}

func (a *Bootstrap) addBootkubeFiles(dependencies asset.Parents, templateData *bootstrapTemplateData) {
	bootkubeConfigOverridesDir := filepath.Join(rootDir, "bootkube-config-overrides")
	adminKubeconfig := &kubeconfig.Admin{}
	manifests := &manifests.Manifests{}
	dependencies.Get(adminKubeconfig, manifests)

	a.Config.Storage.Files = append(
		a.Config.Storage.Files,
		ignition.FileFromString("/usr/local/bin/bootkube.sh", 0555, applyTemplateData(content.BootkubeShFileTemplate, templateData)),
	)
	for _, o := range content.BootkubeConfigOverrides {
		a.Config.Storage.Files = append(
			a.Config.Storage.Files,
			ignition.FileFromString(filepath.Join(bootkubeConfigOverridesDir, o.Name()), 0600, applyTemplateData(o, templateData)),
		)
	}
	a.Config.Storage.Files = append(
		a.Config.Storage.Files,
		ignition.FilesFromAsset(rootDir, 0600, adminKubeconfig)...,
	)
	a.Config.Storage.Files = append(
		a.Config.Storage.Files,
		ignition.FilesFromAsset(rootDir, 0644, manifests)...,
	)
}

func (a *Bootstrap) addTemporaryBootkubeFiles(templateData *bootstrapTemplateData) {
	podCheckpointerBootstrapDir := filepath.Join(rootDir, "pod-checkpointer-operator-bootstrap")
	for name, data := range content.PodCheckpointerBootkubeManifests {
		a.Config.Storage.Files = append(
			a.Config.Storage.Files,
			ignition.FileFromString(filepath.Join(podCheckpointerBootstrapDir, name), 0644, data),
		)
	}

	kubeProxyBootstrapDir := filepath.Join(rootDir, "kube-proxy-operator-bootstrap")
	for name, data := range content.KubeProxyBootkubeManifests {
		a.Config.Storage.Files = append(
			a.Config.Storage.Files,
			ignition.FileFromString(filepath.Join(kubeProxyBootstrapDir, name), 0644, data),
		)
	}

	kubeDNSBootstrapDir := filepath.Join(rootDir, "kube-dns-operator-bootstrap")
	for name, data := range content.KubeDNSBootkubeManifests {
		a.Config.Storage.Files = append(
			a.Config.Storage.Files,
			ignition.FileFromString(filepath.Join(kubeDNSBootstrapDir, name), 0644, data),
		)
	}
	a.Config.Storage.Files = append(
		a.Config.Storage.Files,
		ignition.FileFromString(filepath.Join(kubeDNSBootstrapDir, "kube-dns-svc.yaml"), 0644, applyTemplateData(content.BootkubeKubeDNSService, templateData)),
	)
}

func (a *Bootstrap) addTectonicFiles(dependencies asset.Parents) {
	tectonic := &manifests.Tectonic{}
	dependencies.Get(tectonic)

	a.Config.Storage.Files = append(
		a.Config.Storage.Files,
		ignition.FileFromString("/usr/local/bin/tectonic.sh", 0555, content.TectonicShFileContents),
	)
	a.Config.Storage.Files = append(
		a.Config.Storage.Files,
		ignition.FilesFromAsset(rootDir, 0644, tectonic)...,
	)
}

func (a *Bootstrap) addTLSCertFiles(dependencies asset.Parents) {
	for _, asset := range []asset.WritableAsset{
		&tls.RootCA{},
		&tls.KubeCA{},
		&tls.AggregatorCA{},
		&tls.ServiceServingCA{},
		&tls.EtcdCA{},
		&tls.ClusterAPIServerCertKey{},
		&tls.EtcdClientCertKey{},
		&tls.APIServerCertKey{},
		&tls.OpenshiftAPIServerCertKey{},
		&tls.APIServerProxyCertKey{},
		&tls.AdminCertKey{},
		&tls.KubeletCertKey{},
		&tls.MCSCertKey{},
		&tls.ServiceAccountKeyPair{},
	} {
		dependencies.Get(asset)
		a.Config.Storage.Files = append(a.Config.Storage.Files, ignition.FilesFromAsset(rootDir, 0600, asset)...)
	}

	etcdClientCertKey := &tls.EtcdClientCertKey{}
	dependencies.Get(etcdClientCertKey)
	a.Config.Storage.Files = append(
		a.Config.Storage.Files,
		ignition.FileFromBytes("/etc/ssl/etcd/ca.crt", 0600, etcdClientCertKey.Cert()),
	)
}

func applyTemplateData(template *template.Template, templateData interface{}) string {
	buf := &bytes.Buffer{}
	if err := template.Execute(buf, templateData); err != nil {
		panic(err)
	}
	return buf.String()
}

// Load returns the bootstrap ignition from disk.
func (a *Bootstrap) Load(f asset.FileFetcher) (found bool, err error) {
	file, err := f.FetchByName(bootstrapIgnFilename)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	config := &igntypes.Config{}
	if err := json.Unmarshal(file.Data, config); err != nil {
		return false, errors.Wrapf(err, "failed to unmarshal")
	}

	a.File, a.Config = file, config
	return true, nil
}
