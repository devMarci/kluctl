package deployment

import (
	"context"
	"github.com/kluctl/kluctl/v2/pkg/git/repoprovider"
	"github.com/kluctl/kluctl/v2/pkg/k8s"
	"github.com/kluctl/kluctl/v2/pkg/vars"
)

type SharedContext struct {
	Ctx        context.Context
	K          *k8s.K8sCluster
	RP         repoprovider.RepoProvider
	VarsLoader *vars.VarsLoader

	RenderDir                         string
	SealedSecretsDir                  string
	DefaultSealedSecretsOutputPattern string
}