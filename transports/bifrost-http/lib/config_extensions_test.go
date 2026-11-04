package lib

// The declared-extension wiring seam (07.1b, F-FIN-1): GET /api/extensions is served from
// Config.GetDeclaredExtensions, which reads the loaded plugin instances out of the real
// BasePlugins registry and asks each one for its optional extension descriptor. The handler
// tests pin the manifest's shape against a fake loader, so the one thing they cannot see is
// this read: which registry the manifest comes from, and that only a plugin implementing the
// descriptor contributes. These tests register stubs through ReloadPlugin - the same entry
// point the server's plugin loading uses - and assert what the registry yields.

import (
	"testing"

	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// descriptorPlugin is a loaded plugin that declares an extension through the optional
// interface: the stand-in for the governance plugin, whose credit-admission descriptor is the
// only implementation of declaredExtension in the build.
type descriptorPlugin struct {
	name       string
	descriptor governance.CreditAdmissionDescriptor
}

func (p *descriptorPlugin) GetName() string { return p.name }
func (p *descriptorPlugin) Cleanup() error  { return nil }

func (p *descriptorPlugin) CreditAdmissionExtension() governance.CreditAdmissionDescriptor {
	return p.descriptor
}

// A plugin loaded through the registry that implements the descriptor declares its extension;
// a plugin loaded beside it that does not implements nothing and contributes no declaration.
func TestGetDeclaredExtensionsReadsTheLoadedPlugins(t *testing.T) {
	config := newTestConfigForPlugins()
	require.NoError(t, config.ReloadPlugin(&mockPlugin{name: "otel"}))
	require.NoError(t, config.ReloadPlugin(&descriptorPlugin{
		name: "governance",
		descriptor: governance.CreditAdmissionDescriptor{
			Name:    "openbot-credit-admission",
			Version: "1.0.0",
			Enabled: true,
		},
	}))

	got := config.GetDeclaredExtensions()

	require.Len(t, got, 1,
		"exactly the plugin implementing the descriptor declares an extension; a plugin without one contributes nothing")
	assert.Equal(t, ExtensionDeclaration{
		PluginName: "governance",
		Name:       "openbot-credit-admission",
		Version:    "1.0.0",
		Enabled:    true,
	}, got[0], "the declaration is the loaded instance's descriptor, attributed to the plugin it came from")
}

// The descriptor is reported as the plugin holds it, including a disabled switch: the manifest
// endpoint is what filters on Enabled, so the registry must not pre-filter and hide the state
// the negotiation's "would this deployment honor it" answer rests on.
func TestGetDeclaredExtensionsCarriesTheDescriptorVerbatim(t *testing.T) {
	config := newTestConfigForPlugins()
	require.NoError(t, config.ReloadPlugin(&descriptorPlugin{
		name: "governance",
		descriptor: governance.CreditAdmissionDescriptor{
			Name:    "openbot-credit-admission",
			Version: "2.3.4",
			Enabled: false,
		},
	}))

	got := config.GetDeclaredExtensions()

	require.Len(t, got, 1)
	assert.Equal(t, "openbot-credit-admission", got[0].Name)
	assert.Equal(t, "2.3.4", got[0].Version)
	assert.False(t, got[0].Enabled, "a disabled extension is still declared, with Enabled=false")
}

// A reload replaces the instance the registry reports, so the declaration follows the live
// plugin rather than a stale one - the registry is read per call, not cached at load time.
func TestGetDeclaredExtensionsFollowsAReloadedPlugin(t *testing.T) {
	config := newTestConfigForPlugins()
	require.NoError(t, config.ReloadPlugin(&descriptorPlugin{
		name: "governance",
		descriptor: governance.CreditAdmissionDescriptor{
			Name: "openbot-credit-admission", Version: "1.0.0", Enabled: true,
		},
	}))
	require.NoError(t, config.ReloadPlugin(&descriptorPlugin{
		name: "governance",
		descriptor: governance.CreditAdmissionDescriptor{
			Name: "openbot-credit-admission", Version: "1.0.0", Enabled: false,
		},
	}))

	got := config.GetDeclaredExtensions()

	require.Len(t, got, 1, "the replaced instance is the only governance plugin in the registry")
	assert.False(t, got[0].Enabled,
		"the declaration is read from the instance the registry now holds, not the one it replaced")
}

// Nothing loaded declares nothing: a deployment that loaded no plugins yields no declarations
// (the handler turns that into the empty manifest shape).
func TestGetDeclaredExtensionsWithoutLoadedPlugins(t *testing.T) {
	config := newTestConfigForPlugins()

	assert.Empty(t, config.GetDeclaredExtensions(),
		"an unloaded registry declares no extension")
}
