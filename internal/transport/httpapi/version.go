package httpapi

import (
	"context"

	"github.com/The-Vibe-Company/quivr/internal/buildinfo"
	"github.com/The-Vibe-Company/quivr/internal/plugins"
	transport "github.com/The-Vibe-Company/quivr/internal/transport/generated"
)

func (a *API) GetBuildVersion(context.Context, transport.GetBuildVersionRequestObject) (transport.GetBuildVersionResponseObject, error) {
	return transport.GetBuildVersion200JSONResponse{
		Version: buildinfo.Version, Revision: buildinfo.Revision,
		ApiVersion: transport.V0, PluginEngineVersion: plugins.EngineVersion,
	}, nil
}
