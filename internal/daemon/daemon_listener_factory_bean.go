package daemon

// @import { Bean, Singleton } from "github.com/spice-framework/spice/annotation/core"

// NewListenerFactory contributes the production current-user local IPC
// listener through ordinary generated constructor injection.
//
// @Bean(name="daemonListenerFactory")
// @Singleton
func NewListenerFactory() ListenerFactory { return localListenerFactory{} }
