import logging
import os
import shutil
import subprocess
import sys
from tempfile import TemporaryDirectory

from git import Git
from pytest_kind import KindCluster

from kluctl.utils.yaml_utils import yaml_save_file, yaml_load_file

logger = logging.getLogger(__name__)


class KluctlTestProject:
    def __init__(self, kluctl_project_external=False,
                 clusters_external=False, deployment_external=False, sealed_secrets_external=False,
                 local_clusters=None, local_deployment=None, local_sealed_secrets=None):
        self.kluctl_project_external = kluctl_project_external
        self.clusters_external = clusters_external
        self.deployment_external = deployment_external
        self.sealed_secrets_external = sealed_secrets_external
        self.local_clusters = local_clusters
        self.local_deployment = local_deployment
        self.local_sealed_secrets = local_sealed_secrets
        self.kubeconfigs = []

    def __enter__(self):
        self.base_dir = TemporaryDirectory()

        os.makedirs(self.get_kluctl_project_dir(), exist_ok=True)
        os.makedirs(os.path.join(self.get_clusters_dir(), "clusters"), exist_ok=True)
        os.makedirs(os.path.join(self.get_sealed_secrets_dir(), ".sealed-secrets"), exist_ok=True)
        os.makedirs(self.get_deployment_dir(), exist_ok=True)

        self._git_init(self.get_kluctl_project_dir())
        if self.clusters_external:
            self._git_init(self.get_clusters_dir())
        if self.deployment_external:
            self._git_init(self.get_deployment_dir())
        if self.sealed_secrets_external:
            self._git_init(self.get_sealed_secrets_dir())

        def do_update_kluctl(y):
            if self.clusters_external:
                y["clusters"] = {
                    "project": "file://%s" % self.get_clusters_dir(),
                }
            if self.deployment_external:
                y["deployment"] = {
                    "project": "file://%s" % self.get_deployment_dir(),
                }
            if self.sealed_secrets_external:
                y["sealedSecrets"] = {
                    "project": "file://%s" % self.get_sealed_secrets_dir(),
                }
            return y

        self.update_kluctl_yaml(do_update_kluctl)
        self.update_deployment_yaml(".", lambda y: y)

        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        return self.base_dir.__exit__(exc_type, exc_val, exc_tb)

    def _git_init(self, dir):
        os.makedirs(dir, exist_ok=True)
        g = Git(dir)
        g.init()
        with open(os.path.join(dir, ".dummy"), "wt") as f:
            f.write("dummy")
        g.add(".dummy")
        g.commit("-m", "initial")

    def _commit(self, dir, add=None, all=None, message="commit"):
        assert self.base_dir.name in dir

        g = Git(dir)

        if add is not None:
            for f in add:
                g.add(f)

        if all or (add is None and all is None):
            g.add(".")

        g.commit("-m", message)

    def _commit_yaml(self, y, path, message="save yaml"):
        yaml_save_file(y, path)
        self._commit(os.path.dirname(path), add=[os.path.basename(path)], message=message)

    def update_yaml(self, path, update, message=None):
        assert self.base_dir.name in path
        try:
            y = yaml_load_file(path)
        except:
            y = {}
        y = update(y)
        yaml_save_file(y, path)

        if message is None:
            message = "update %s" % os.path.relpath(path, self.base_dir.name)
        self._commit_yaml(y, path, message=message)

    def update_kluctl_yaml(self, update):
        self.update_yaml(os.path.join(self.get_kluctl_project_dir(), ".kluctl.yml"), update)

    def update_deployment_yaml(self, dir, update):
        self.update_yaml(os.path.join(self.get_deployment_dir(), dir, "deployment.yml"), update)

    def copy_deployment(self, source):
        shutil.copytree(source, self.get_deployment_dir())
        self._commit(self.get_deployment_dir(), all=True, message="copy deployment from %s" % source)

    def copy_kustomize_dir(self, source, target, add_to_deployment):
        p = os.path.join(self.get_deployment_dir(), target)
        shutil.copytree(source, p)
        self._commit(p, all=True, message="copy kustomize dir from %s to %s" % (source, target))

    def add_cluster(self, name, context, vars):
        y = {
            "cluster": {
                "name": name,
                "context": context,
                **vars,
            }
        }
        self._commit_yaml(y, os.path.join(self.get_clusters_dir(), "clusters", "%s.yml" % name), message="add cluster")

    def add_kind_cluster(self, kind_cluster: KindCluster, vars={}):
        if kind_cluster.kubeconfig_path not in self.kubeconfigs:
            self.kubeconfigs.append(kind_cluster.kubeconfig_path)
        context = kind_cluster.kubectl("config", "current-context").strip()
        self.add_cluster(kind_cluster.name, context, vars)

    def add_target(self, name, cluster, args={}):
        def do_update(y):
            targets = y.setdefault("targets", [])
            targets.append({
                "name": name,
                "cluster": cluster,
                "args": args,
            })
            return y
        self.update_kluctl_yaml(do_update)

    def add_deployment_include(self, dir, include):
        def do_update(y):
            includes = y.setdefault("includes", [])
            if any(x["path" == include] for x in includes):
                return
            includes.append({
                "path": include,
            })
            return y
        self.update_deployment_yaml(dir, do_update)

    def add_deployment_includes(self, dir):
        p = ["."]
        for x in os.path.split(dir):
            self.add_deployment_include(os.path.join(*p), x)
            p.append(x)

    def add_kustomize_deployment(self, dir, resources):
        deployment_dir = os.path.dirname(dir)
        if deployment_dir != "":
            self.add_deployment_includes(deployment_dir)

        abs_deployment_dir = os.path.join(self.get_deployment_dir(), deployment_dir)
        abs_kustomize_dir = os.path.join(self.get_deployment_dir(), dir)

        os.makedirs(abs_kustomize_dir, exist_ok=True)

        for name, content in resources.items():
            with open(os.path.join(abs_kustomize_dir, name), "wt") as f:
                f.write(content)

        y = {
            "apiVersion": "kustomize.config.k8s.io/v1beta1",
            "kind": "Kustomization",
            "resources": list(resources.keys()),
        }
        yaml_save_file(y, os.path.join(abs_kustomize_dir, "kustomization.yml"))
        self._commit(abs_deployment_dir, all=True, message="add kustomize deployment %s" % dir)

        def do_update(y):
            kustomize_dirs = y.setdefault("kustomizeDirs", [])
            kustomize_dirs.append({
                "path": os.path.basename(dir)
            })
            return y
        self.update_deployment_yaml(deployment_dir, do_update)

    def get_kluctl_project_dir(self):
        return os.path.join(self.base_dir.name, "kluctl-project")

    def get_clusters_dir(self):
        if self.clusters_external:
            return os.path.join(self.base_dir.name, "external-clusters")
        else:
            return self.get_kluctl_project_dir()

    def get_deployment_dir(self):
        if self.deployment_external:
            return os.path.join(self.base_dir.name, "external-deployment")
        else:
            return self.get_kluctl_project_dir()

    def get_sealed_secrets_dir(self):
        if self.sealed_secrets_external:
            return os.path.join(self.base_dir.name, "external-sealed-secrets")
        else:
            return self.get_kluctl_project_dir()

    def kluctl(self, *args: str, **kwargs):
        kluctl_path = os.path.join(os.path.dirname(__file__), "..", "..", "cli.py")
        args2 = [kluctl_path]
        args2 += args

        cwd = None
        if self.kluctl_project_external:
            args2 += ["--project-url", "file://%s" % self.get_kluctl_project_dir()]
        else:
            cwd = self.get_kluctl_project_dir()

        if self.local_clusters:
            args2 += ["--local-clusters", self.local_clusters]
        if self.local_deployment:
            args2 += ["--local-deployment", self.local_deployment]
        if self.local_sealed_secrets:
            args2 += ["--local-sealed-secrets", self.local_sealed_secrets]

        sep = ";" if sys.platform == "win32" else ":"
        env = os.environ.copy()
        env.update({
            "KUBECONFIG": sep.join(os.path.abspath(str(x)) for x in self.kubeconfigs),
        })

        logger.info("Running kluctl: %s" % " ".join(args2[1:]))

        return subprocess.check_output(
            args2,
            cwd=cwd,
            env=env,
            encoding="utf-8",
            **kwargs,
        )