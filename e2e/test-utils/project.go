package test_utils

import (
	"fmt"
	"github.com/go-git/go-git/v5"
	"github.com/huandu/xstrings"
	"github.com/imdario/mergo"
	"github.com/jinzhu/copier"
	git2 "github.com/kluctl/kluctl/v2/pkg/git"
	"github.com/kluctl/kluctl/v2/pkg/utils/uo"
	"github.com/kluctl/kluctl/v2/pkg/yaml"
	registry2 "helm.sh/helm/v3/pkg/registry"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type TestProject struct {
	t        *testing.T
	extraEnv []string

	mergedKubeconfig string

	gitServer *git2.TestGitServer
}

func NewTestProject(t *testing.T, k *EnvTestCluster) *TestProject {
	p := &TestProject{
		t: t,
	}

	p.gitServer = git2.NewTestGitServer(t)
	p.gitServer.GitInit("kluctl-project")

	p.UpdateKluctlYaml(func(o *uo.UnstructuredObject) error {
		return nil
	})
	p.UpdateDeploymentYaml(".", func(c *uo.UnstructuredObject) error {
		return nil
	})

	tmpFile, err := os.CreateTemp("", p.TestSlug()+"-kubeconfig-")
	if err != nil {
		t.Fatal(err)
	}
	_ = tmpFile.Close()
	p.mergedKubeconfig = tmpFile.Name()
	t.Cleanup(func() {
		os.Remove(p.mergedKubeconfig)
	})
	if k != nil {
		p.MergeKubeconfig(k)
	}
	return p
}

func (p *TestProject) TestSlug() string {
	n := p.t.Name()
	n = xstrings.ToKebabCase(n)
	n = strings.ReplaceAll(n, "/", "-")
	return n
}

func (p *TestProject) MergeKubeconfig(k *EnvTestCluster) {
	p.UpdateMergedKubeconfig(func(config *clientcmdapi.Config) {
		nkcfg, err := clientcmd.Load(k.Kubeconfig)
		if err != nil {
			p.t.Fatal(err)
		}

		err = mergo.Merge(config, nkcfg)
		if err != nil {
			p.t.Fatal(err)
		}
	})
}

func (p *TestProject) UpdateMergedKubeconfig(cb func(config *clientcmdapi.Config)) {
	mkcfg, err := clientcmd.LoadFromFile(p.mergedKubeconfig)
	if err != nil {
		p.t.Fatal(err)
	}

	cb(mkcfg)

	err = clientcmd.WriteToFile(*mkcfg, p.mergedKubeconfig)
	if err != nil {
		p.t.Fatal(err)
	}
}

func (p *TestProject) AddExtraEnv(e string) {
	p.extraEnv = append(p.extraEnv, e)
}

func (p *TestProject) UpdateKluctlYaml(update func(o *uo.UnstructuredObject) error) {
	p.UpdateYaml(".kluctl.yml", update, "")
}

func (p *TestProject) UpdateDeploymentYaml(dir string, update func(o *uo.UnstructuredObject) error) {
	p.UpdateYaml(filepath.Join(dir, "deployment.yml"), func(o *uo.UnstructuredObject) error {
		if dir == "." {
			o.SetNestedField(p.TestSlug(), "commonLabels", "project_name")
		}
		return update(o)
	}, "")
}

func (p *TestProject) UpdateYaml(path string, update func(o *uo.UnstructuredObject) error, message string) {
	p.gitServer.UpdateYaml("kluctl-project", path, func(o map[string]any) error {
		u := uo.FromMap(o)
		err := update(u)
		if err != nil {
			return err
		}
		_ = copier.CopyWithOption(&o, &u.Object, copier.Option{DeepCopy: true})
		return nil
	}, message)
}

func (p *TestProject) UpdateFile(path string, update func(f string) (string, error), message string) {
	p.gitServer.UpdateFile("kluctl-project", path, update, message)
}

func (p *TestProject) GetDeploymentYaml(dir string) *uo.UnstructuredObject {
	o, err := uo.FromFile(filepath.Join(p.LocalRepoDir(), dir, "deployment.yml"))
	if err != nil {
		p.t.Fatal(err)
	}
	return o
}

func (p *TestProject) ListDeploymentItemPathes(dir string, fullPath bool) []string {
	var ret []string
	o := p.GetDeploymentYaml(dir)
	l, _, err := o.GetNestedObjectList("deployments")
	if err != nil {
		p.t.Fatal(err)
	}
	for _, x := range l {
		pth, ok, _ := x.GetNestedString("path")
		if ok {
			x := pth
			if fullPath {
				x = filepath.Join(dir, pth)
			}
			ret = append(ret, x)
		}
		pth, ok, _ = x.GetNestedString("include")
		if ok {
			ret = append(ret, p.ListDeploymentItemPathes(filepath.Join(dir, pth), fullPath)...)
		}
	}
	return ret
}

func (p *TestProject) UpdateKustomizeDeployment(dir string, update func(o *uo.UnstructuredObject, wt *git.Worktree) error) {
	wt := p.gitServer.GetWorktree("kluctl-project")

	pth := filepath.Join(dir, "kustomization.yml")
	p.UpdateYaml(pth, func(o *uo.UnstructuredObject) error {
		return update(o, wt)
	}, fmt.Sprintf("Update kustomization.yml for %s", dir))
}

func (p *TestProject) UpdateTarget(name string, cb func(target *uo.UnstructuredObject)) {
	p.UpdateNamedListItem(uo.KeyPath{"targets"}, name, cb)
}

func (p *TestProject) UpdateSecretSet(name string, cb func(secretSet *uo.UnstructuredObject)) {
	p.UpdateNamedListItem(uo.KeyPath{"secretsConfig", "secretSets"}, name, cb)
}

func (p *TestProject) UpdateNamedListItem(path uo.KeyPath, name string, cb func(item *uo.UnstructuredObject)) {
	if cb == nil {
		cb = func(target *uo.UnstructuredObject) {}
	}

	p.UpdateKluctlYaml(func(o *uo.UnstructuredObject) error {
		l, _, _ := o.GetNestedObjectList(path...)
		var newList []*uo.UnstructuredObject
		found := false
		for _, item := range l {
			n, _, _ := item.GetNestedString("name")
			if n == name {
				cb(item)
				found = true
			}
			newList = append(newList, item)
		}
		if !found {
			n := uo.FromMap(map[string]interface{}{
				"name": name,
			})
			cb(n)
			newList = append(newList, n)
		}

		_ = o.SetNestedObjectList(newList, path...)
		return nil
	})
}

func (p *TestProject) UpdateDeploymentItems(dir string, update func(items []*uo.UnstructuredObject) []*uo.UnstructuredObject) {
	p.UpdateDeploymentYaml(dir, func(o *uo.UnstructuredObject) error {
		items, _, _ := o.GetNestedObjectList("deployments")
		items = update(items)
		return o.SetNestedField(items, "deployments")
	})
}

func (p *TestProject) AddDeploymentItem(dir string, item *uo.UnstructuredObject) {
	p.UpdateDeploymentItems(dir, func(items []*uo.UnstructuredObject) []*uo.UnstructuredObject {
		for _, x := range items {
			if reflect.DeepEqual(x, item) {
				return items
			}
		}
		items = append(items, item)
		return items
	})
}

func (p *TestProject) AddDeploymentInclude(dir string, includePath string, tags []string) {
	n := uo.FromMap(map[string]interface{}{
		"include": includePath,
	})
	if len(tags) != 0 {
		n.SetNestedField(tags, "tags")
	}
	p.AddDeploymentItem(dir, n)
}

func (p *TestProject) AddDeploymentIncludes(dir string) {
	var pp []string
	for _, x := range strings.Split(dir, "/") {
		if x != "." {
			p.AddDeploymentInclude(filepath.Join(pp...), x, nil)
		}
		pp = append(pp, x)
	}
}

func (p *TestProject) AddKustomizeDeployment(dir string, resources []KustomizeResource, tags []string) {
	deploymentDir := filepath.Dir(dir)
	if deploymentDir != "" {
		p.AddDeploymentIncludes(deploymentDir)
	}

	absKustomizeDir := filepath.Join(p.LocalRepoDir(), dir)

	err := os.MkdirAll(absKustomizeDir, 0o700)
	if err != nil {
		p.t.Fatal(err)
	}

	p.UpdateKustomizeDeployment(dir, func(o *uo.UnstructuredObject, wt *git.Worktree) error {
		o.SetNestedField("kustomize.config.k8s.io/v1beta1", "apiVersion")
		o.SetNestedField("Kustomization", "kind")
		return nil
	})

	p.AddKustomizeResources(dir, resources)
	p.UpdateDeploymentYaml(deploymentDir, func(o *uo.UnstructuredObject) error {
		d, _, _ := o.GetNestedObjectList("deployments")
		n := uo.FromMap(map[string]interface{}{
			"path": filepath.Base(dir),
		})
		if len(tags) != 0 {
			n.SetNestedField(tags, "tags")
		}
		d = append(d, n)
		_ = o.SetNestedObjectList(d, "deployments")
		return nil
	})
}

func (p *TestProject) AddHelmDeployment(dir string, repoUrl string, chartName, version string, releaseName string, namespace string, values map[string]any) {
	if registry2.IsOCI(repoUrl) {
		repoUrl += "/" + chartName
		chartName = ""
	}

	p.AddKustomizeDeployment(dir, []KustomizeResource{
		{Name: "helm-rendered.yaml"},
	}, nil)

	p.UpdateYaml(filepath.Join(dir, "helm-chart.yaml"), func(o *uo.UnstructuredObject) error {
		*o = *uo.FromMap(map[string]interface{}{
			"helmChart": map[string]any{
				"repo":         repoUrl,
				"chartVersion": version,
				"releaseName":  releaseName,
				"namespace":    namespace,
			},
		})
		if chartName != "" {
			_ = o.SetNestedField(chartName, "helmChart", "chartName")
		}
		return nil
	}, "")

	if values != nil {
		p.UpdateYaml(filepath.Join(dir, "helm-values.yaml"), func(o *uo.UnstructuredObject) error {
			*o = *uo.FromMap(values)
			return nil
		}, "")
	}
}

func (p *TestProject) convertInterfaceToList(x interface{}) []interface{} {
	var ret []interface{}
	if l, ok := x.([]interface{}); ok {
		return l
	}
	if l, ok := x.([]*uo.UnstructuredObject); ok {
		for _, y := range l {
			ret = append(ret, y)
		}
		return ret
	}
	if l, ok := x.([]map[string]interface{}); ok {
		for _, y := range l {
			ret = append(ret, y)
		}
		return ret
	}
	return []interface{}{x}
}

type KustomizeResource struct {
	Name     string
	FileName string
	Content  interface{}
}

func (p *TestProject) AddKustomizeResources(dir string, resources []KustomizeResource) {
	p.UpdateKustomizeDeployment(dir, func(o *uo.UnstructuredObject, wt *git.Worktree) error {
		l, _, _ := o.GetNestedList("resources")
		for _, r := range resources {
			l = append(l, r.Name)
			fileName := r.FileName
			if fileName == "" {
				fileName = r.Name
			}
			if r.Content != nil {
				x := p.convertInterfaceToList(r.Content)
				err := yaml.WriteYamlAllFile(filepath.Join(p.LocalRepoDir(), dir, fileName), x)
				if err != nil {
					return err
				}
				_, err = wt.Add(filepath.Join(dir, fileName))
				if err != nil {
					return err
				}
			}
		}
		o.SetNestedField(l, "resources")
		return nil
	})
}

func (p *TestProject) DeleteKustomizeDeployment(dir string) {
	deploymentDir := filepath.Dir(dir)
	p.UpdateDeploymentItems(deploymentDir, func(items []*uo.UnstructuredObject) []*uo.UnstructuredObject {
		var newItems []*uo.UnstructuredObject
		for _, item := range items {
			pth, _, _ := item.GetNestedString("path")
			if pth == filepath.Base(dir) {
				continue
			}
			newItems = append(newItems, item)
		}
		return newItems
	})
}

func (p *TestProject) GitUrl() string {
	return p.gitServer.GitUrl("kluctl-project")
}

func (p *TestProject) LocalRepoDir() string {
	return p.gitServer.LocalRepoDir("kluctl-project")
}

func (p *TestProject) GetGitRepo() *git.Repository {
	return p.gitServer.GetGitRepo("kluctl-project")
}

func (p *TestProject) Kluctl(argsIn ...string) (string, string, error) {
	var args []string
	args = append(args, argsIn...)
	args = append(args, "--no-update-check")

	cwd := p.LocalRepoDir()

	args = append(args, "--debug")

	env := os.Environ()
	env = append(env, p.extraEnv...)
	env = append(env, fmt.Sprintf("KUBECONFIG=%s", p.mergedKubeconfig))

	// this will cause the init() function from call_kluctl_hack.go to invoke the kluctl root command and then exit
	env = append(env, "CALL_KLUCTL=true")
	env = append(env, fmt.Sprintf("KLUCTL_BASE_TMP_DIR=%s", p.t.TempDir()))

	p.t.Logf("Runnning kluctl: %s", strings.Join(args, " "))

	testExe, err := os.Executable()
	if err != nil {
		panic(err)
	}

	cmd := exec.Command(testExe, args...)
	cmd.Dir = cwd
	cmd.Env = env

	stdout, stderr, err := runHelper(p.t, cmd)
	return stdout, stderr, err
}

func (p *TestProject) KluctlMust(argsIn ...string) (string, string) {
	stdout, stderr, err := p.Kluctl(argsIn...)
	if err != nil {
		p.t.Logf(stderr)
		p.t.Fatal(fmt.Errorf("kluctl failed: %w", err))
	}
	return stdout, stderr
}