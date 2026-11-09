// Command sessionguard is the shared-object build of the session guard plugin.
//
// Build it with -buildmode=plugin (see the Makefile) and point a plugin entry in Bifrost's
// config.json at the resulting .so file. Bifrost looks the exported functions below up by
// name; each one hands over to the plugin value created in Init.
package main

import (
	"github.com/maximhq/bifrost/core/schemas"
	sessionguard "github.com/webneco/bifrost-session-guard/plugin"
)

var instance *sessionguard.Plugin

// Init is called once when Bifrost loads the plugin, with the "config" value from config.json.
func Init(config any) error {
	p, err := sessionguard.New(config)
	if err != nil {
		return err
	}
	instance = p
	return nil
}

// GetName returns the plugin's name.
func GetName() string { return sessionguard.PluginName }

// HTTPTransportPreHook prepares the request's context for the hooks below.
func HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if instance == nil {
		return nil, nil
	}
	return instance.HTTPTransportPreHook(ctx, req)
}

// PreLLMHook decides whether the request may reach a provider.
func PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if instance == nil {
		return req, nil, nil
	}
	return instance.PreLLMHook(ctx, req)
}

// PostLLMHook records what the call consumed.
func PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if instance == nil {
		return resp, bifrostErr, nil
	}
	return instance.PostLLMHook(ctx, resp, bifrostErr)
}

// HTTPTransportResponseHeadersHook reports the decision in response headers.
func HTTPTransportResponseHeadersHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
	if instance == nil {
		return nil
	}
	return instance.HTTPTransportResponseHeadersHook(ctx, req, resp)
}

// Cleanup is called when Bifrost shuts down or unloads the plugin.
func Cleanup() error {
	if instance == nil {
		return nil
	}
	err := instance.Cleanup()
	instance = nil
	return err
}

// main is never run: Bifrost loads this package as a plugin. It exists so that ordinary
// tooling (go build ./..., go vet) accepts the package.
func main() {}
